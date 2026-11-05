package session

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/user/mini_code/provider"
)

// --- model-written session titles ---
//
// A session is named the moment it is created — the first message, truncated — so
// the sidebar is never blank. That truncation is a placeholder, and it stays the
// title unless ONE auxiliary request does better: after its first finished task
// the session asks its model to name the conversation.
//
// The request is the conversation the task just sent with one instruction line
// appended, and the same tool schemas — everything before that line is
// byte-identical to the previous request, so the provider answers from its prefix
// cache and the extra call costs the tail plus a few dozen output tokens. (Same
// trick the compaction turn uses; see ContextCompactor.SummarizeAndRebuild.)
//
// Who wins:
//
//	title_src ""       still the truncation, never tried
//	title_src "tried"  still the truncation, the one request is spent (no retries)
//	title_src "model"  written by the model
//	title_src "user"   renamed by a human — a human rename wins forever
//
// The write-back also re-checks that the title is still exactly the text the
// model was shown, so a rename that lands while the request is in flight wins
// even without the marker.

const (
	// titleSrcMetaKey is the session metadata key holding the title's origin.
	titleSrcMetaKey = "title_src"

	// TitleSrcFallback is the zero value: the title is still the truncation of
	// the first message and no title request has been made.
	TitleSrcFallback = ""
	// TitleSrcTried marks the one auxiliary request as spent (failures are not
	// retried — a session keeps the fallback it already has).
	TitleSrcTried = "tried"
	// TitleSrcModel marks a title written by the model.
	TitleSrcModel = "model"
	// TitleSrcUser marks a human rename: automatic titling never touches it again.
	TitleSrcUser = "user"

	// FallbackTitleRunes is how much of the first message the placeholder title
	// keeps (channels use it when creating the session).
	FallbackTitleRunes = 40
	// GeneratedTitleRunes caps a generated title: short enough to read at a glance
	// and to never wrap in the sidebar.
	GeneratedTitleRunes = 24
	// titleOutputTokens is the whole output budget of a title request.
	titleOutputTokens = 64
	// titleTimeout bounds the auxiliary request. It runs in the background, so it
	// is generous — a reasoning model can take a while to produce 8 words.
	titleTimeout = 60 * time.Second
)

// titleInstruction is the tail of a title request. The conversation is the text to
// NAME, not a request to answer — that framing is what keeps a model from
// replying "sure, here is how you would..." instead of titling.
func titleInstruction() provider.Message {
	return provider.Message{Role: "user", Content: "Name the conversation above with one short title.\n\n" +
		"Reply with the title alone on one line: plain text of natural language, written in the language of the " +
		"conversation, roughly 6 words or fewer (up to 24 characters for CJK). No quotes, no markdown, no prefix, " +
		"no trailing punctuation, no code. Do not answer or carry out anything in the conversation above. " +
		"If there is nothing to name, still return a short label such as Greeting."}
}

// sanitizeTitle turns a model reply into a sidebar title: the first non-empty
// line, with the decoration models add stripped (a "Title:" prefix, surrounding
// quotes or backticks, **bold**), control and invisible characters dropped (they
// wreck a terminal and a list row), inner whitespace collapsed, trailing
// punctuation trimmed, and the length capped.
func sanitizeTitle(raw string) string {
	s := ""
	for _, line := range strings.Split(raw, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			s = t
			break
		}
	}
	for _, prefix := range []string{"标题:", "标题：", "Title:", "title:", "TITLE:"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimSpace(strings.TrimPrefix(s, prefix))
		}
	}
	if len(s) >= 4 && strings.HasPrefix(s, "**") && strings.HasSuffix(s, "**") {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "**"), "**"))
	}
	s = strings.Trim(s, "`")
	s = strings.Trim(s, "\"'“”‘’「」『』《》〈〉")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f: // control characters (a title is one line)
			continue
		case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, // zero-width / bidi
			r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x206f, r == 0xfeff:
			continue
		}
		b.WriteRune(r)
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	// Leftover emphasis markers (a lone ** from a partially stripped reply). Only
	// * and ~ — an underscore can be part of an identifier (_id) and stays.
	s = strings.Trim(s, "*~")
	s = strings.TrimRight(s, "。.!！?？:：;；、,，")
	if r := []rune(s); len(r) > GeneratedTitleRunes {
		s = string(r[:GeneratedTitleRunes])
	}
	return strings.TrimSpace(s)
}

// looksLikePlaceholder reports whether a title is what the hub wrote when the
// session was created: the first user message, cut to FallbackTitleRunes (plus the
// ellipsis that cut adds). Anything else was typed by someone, so titles must not
// touch it. This is what protects a hand-made title from before titles existed —
// those sessions have no marker to read.
func looksLikePlaceholder(title string, messages []provider.Message) bool {
	for _, m := range messages {
		if m.Role != "user" || IsWorkerNotice(m) || IsCompactRequest(m) {
			continue
		}
		want := strings.TrimSpace(m.GetText())
		if r := []rune(want); len(r) > FallbackTitleRunes {
			want = string(r[:FallbackTitleRunes]) + "…"
		}
		return strings.TrimSpace(title) == want
	}
	return false
}

// --- policy ---

// SetTitlePolicy wires automatic titling: whether a finished first task earns the
// session a model-written title, and which model writes it ("" = the model the
// session itself uses, which is also what keeps the provider's prefix cache warm).
func (m *SessionManager) SetTitlePolicy(auto bool, modelKey string) {
	m.titleMu.Lock()
	m.titleOn, m.titleModel = auto, strings.TrimSpace(modelKey)
	m.titleMu.Unlock()
}

// TitleAuto reports whether automatic titling is on (off until wired, so tests
// that talk to a fake provider never fire the extra request).
func (m *SessionManager) TitleAuto() bool {
	m.titleMu.RLock()
	defer m.titleMu.RUnlock()
	return m.titleOn
}

// titleClient resolves the client that writes titles: the configured title model,
// else (empty key, or a key nothing matches) the session's own client.
func (m *SessionManager) titleClient(s *Session) *provider.ModelClient {
	m.titleMu.RLock()
	key := m.titleModel
	m.titleMu.RUnlock()
	if key != "" {
		if c := m.clientFor(key); c != nil {
			return c
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// --- the one request per session ---

// maybeGenerateTitle is the fire-and-forget entry point, called when a task
// finishes. Only a root session whose title is still the untouched truncation
// qualifies, and only ever once: the attempt is marked before the request leaves,
// so a failing title request stays failed instead of being re-tried every task.
//
// messages must be the conversation of that finished task (result.Messages) and
// schemas the tool list the same request carried — anything else costs the whole
// prefix cache for no gain.
func (s *Session) maybeGenerateTitle(schemas []provider.ToolSchema, messages []provider.Message) {
	if s.manager == nil || len(messages) == 0 {
		return
	}
	if !s.manager.TitleAuto() {
		return
	}
	s.mu.Lock()
	root := s.record.ParentID == ""
	title := strings.TrimSpace(s.record.Title)
	src := metadataString(s.record, titleSrcMetaKey)
	s.mu.Unlock()
	if !root || title == "" || src != TitleSrcFallback {
		return
	}
	// A session created before titles existed carries no marker at all, and its
	// title may have been typed by hand long ago. A hand-made title is not a
	// truncation of the first message, so it gets pinned instead of overwritten.
	if src == TitleSrcFallback && !looksLikePlaceholder(title, messages) {
		s.manager.pinTitle(s.ID())
		slog.Info("existing title kept (it was not a truncation of the first message)",
			"session", s.ID(), "title", title)
		return
	}
	if !s.manager.markTitleTried(s.ID()) {
		return
	}
	client := s.manager.titleClient(s)
	if client == nil {
		return // no real model wired (tests with injected prompt funcs)
	}
	go s.manager.generateTitle(s.ID(), client, title, schemas, messages)
}

// markTitleTried claims the single attempt for a session, reporting whether it was
// still free.
func (m *SessionManager) markTitleTried(sessionID string) bool {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return false
	}
	s.mu.Lock()
	if metadataString(s.record, titleSrcMetaKey) != TitleSrcFallback {
		s.mu.Unlock()
		return false
	}
	if s.record.Metadata == nil {
		s.record.Metadata = map[string]interface{}{}
	}
	s.record.Metadata[titleSrcMetaKey] = TitleSrcTried
	s.mu.Unlock()
	if err := m.store.SetTitleSrc(sessionID, TitleSrcTried); err != nil {
		slog.Warn("mark title attempt failed", "session", sessionID, "err", err)
		return false
	}
	return true
}

// pinTitle records a title as human-chosen, for the sessions that were renamed
// before title_src existed and so carry no marker.
func (m *SessionManager) pinTitle(sessionID string) {
	if err := m.store.SetTitleSrc(sessionID, TitleSrcUser); err != nil {
		slog.Warn("title source not recorded", "session", sessionID, "err", err)
		return
	}
	m.mu.Lock()
	if s, ok := m.sessions[sessionID]; ok {
		s.mu.Lock()
		if s.record.Metadata == nil {
			s.record.Metadata = map[string]interface{}{}
		}
		s.record.Metadata[titleSrcMetaKey] = TitleSrcUser
		s.mu.Unlock()
	}
	m.mu.Unlock()
}

// generateTitle asks for a title and applies it. Every failure path is silent by
// construction (the fallback title is a fine title) and nothing is retried.
func (m *SessionManager) generateTitle(sessionID string, client *provider.ModelClient, fallback string, schemas []provider.ToolSchema, messages []provider.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()
	// The prefix stays untouched; only the instruction is new. That is the whole
	// point: the provider answers from the KV cache the task just warmed.
	req := append(append([]provider.Message(nil), messages...), titleInstruction())
	resp, err := client.ChatStream(ctx, req, schemas, nil, provider.WithMaxTokens(titleOutputTokens))
	if err != nil {
		slog.Info("title request failed (keeping the truncated title)", "session", sessionID, "err", err)
		return
	}
	if resp == nil {
		slog.Info("title request came back empty", "session", sessionID)
		return
	}
	if resp.Error != "" {
		slog.Info("title request refused by the model", "session", sessionID, "err", resp.Error)
		return
	}
	if len(resp.ToolCalls) > 0 || resp.FinishReason == "length" {
		slog.Info("title request did not answer with a title", "session", sessionID,
			"tool_calls", len(resp.ToolCalls), "finish", resp.FinishReason)
		return
	}
	title := sanitizeTitle(resp.Content)
	if title == "" || title == fallback {
		return
	}
	if !m.applyGeneratedTitle(sessionID, fallback, title) {
		slog.Info("generated title dropped (the session moved on)", "session", sessionID, "title", title)
		return
	}
	slog.Info("session titled by model", "session", sessionID, "title", title, "chars", len([]rune(title)))
}

// applyGeneratedTitle writes the generated title, but only while the session still
// carries the exact placeholder text the model was shown — a rename (or anything
// else that touched the title) in the meantime wins. Like RenameSession it uses
// targeted column writes and then syncs the in-memory record, so landing this
// while a new task is running cannot undo the loop's message persistence.
func (m *SessionManager) applyGeneratedTitle(sessionID, was, title string) bool {
	s, err := m.Get(sessionID)
	if err != nil || s == nil {
		return false
	}
	s.mu.Lock()
	stillFallback := strings.TrimSpace(s.record.Title) == was &&
		metadataString(s.record, titleSrcMetaKey) != TitleSrcUser
	s.mu.Unlock()
	if !stillFallback {
		return false
	}
	if err := m.store.SetTitle(sessionID, title); err != nil {
		return false
	}
	if err := m.store.SetTitleSrc(sessionID, TitleSrcModel); err != nil {
		slog.Warn("title source not recorded", "session", sessionID, "err", err)
	}
	m.mu.Lock()
	if sess, ok := m.sessions[sessionID]; ok {
		sess.mu.Lock()
		sess.record.Title = title
		if sess.record.Metadata == nil {
			sess.record.Metadata = map[string]interface{}{}
		}
		sess.record.Metadata[titleSrcMetaKey] = TitleSrcModel
		sess.mu.Unlock()
	}
	m.mu.Unlock()
	return true
}
