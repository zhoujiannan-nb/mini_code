package session

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/user/mini_code/provider"
)


const (
	titleSrcMetaKey = "title_src"

	TitleSrcFallback = ""
	TitleSrcTried = "tried"
	TitleSrcModel = "model"
	TitleSrcUser = "user"

	FallbackTitleRunes = 40
	GeneratedTitleRunes = 16
	titleInputRunes = 800
	titleOutputTokens = 1024
	titleTimeout = 60 * time.Second
)

func titleRequest(goal string) provider.Message {
	return provider.Message{Role: "user", Content: "Name the request below with a short title.\n\n" +
		"Reply with the title alone on one line: 4-8 words (4 to 8 CJK characters), written in the language of " +
		"the request, plain text — no quotes, no markdown, no prefix, no trailing punctuation, no code. " +
		"Do not answer or carry out the request below.\n\n" +
		"Request:\n" + goal}
}

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
		case r < 0x20 || r == 0x7f:
			continue
		case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e,
			r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x206f, r == 0xfeff:
			continue
		}
		b.WriteRune(r)
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	s = strings.Trim(s, "*~")
	s = strings.TrimRight(s, "。.!！?？:：;；、,，")
	if r := []rune(s); len(r) > GeneratedTitleRunes {
		s = string(r[:GeneratedTitleRunes])
	}
	return strings.TrimSpace(s)
}

func firstTypedMessage(messages []provider.Message) string {
	for _, m := range messages {
		if m.Role != "user" || IsWorkerNotice(m) || IsCompactRequest(m) {
			continue
		}
		return strings.TrimSpace(m.GetText())
	}
	return ""
}

func looksLikePlaceholder(title string, messages []provider.Message) bool {
	want := firstTypedMessage(messages)
	if want == "" {
		return false
	}
	if r := []rune(want); len(r) > FallbackTitleRunes {
		want = string(r[:FallbackTitleRunes]) + "…"
	}
	return strings.TrimSpace(title) == want
}


func (m *SessionManager) SetTitlePolicy(auto bool, modelKey string) {
	m.titleMu.Lock()
	m.titleOn, m.titleModel = auto, strings.TrimSpace(modelKey)
	m.titleMu.Unlock()
}

func (m *SessionManager) TitleAuto() bool {
	m.titleMu.RLock()
	defer m.titleMu.RUnlock()
	return m.titleOn
}

func (m *SessionManager) titleClient(s *Session) *provider.ModelClient {
	m.titleMu.RLock()
	key := m.titleModel
	m.titleMu.RUnlock()
	var base *provider.ModelClient
	if key != "" {
		base = m.clientFor(key)
	}
	if base == nil {
		s.mu.Lock()
		base = s.client
		s.mu.Unlock()
	}
	return base.WithReasoning("disabled", "")
}


func (s *Session) maybeGenerateTitle(messages []provider.Message) {
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
	if src == TitleSrcFallback && !looksLikePlaceholder(title, messages) {
		s.manager.pinTitle(s.ID())
		slog.Info("existing title kept (it was not a truncation of the first message)",
			"session", s.ID(), "title", title)
		return
	}
	goal := firstTypedMessage(messages)
	if goal == "" {
		return
	}
	if !s.manager.markTitleTried(s.ID()) {
		return
	}
	client := s.manager.titleClient(s)
	if client == nil {
		return
	}
	go s.manager.generateTitle(s.ID(), client, title, goal)
}

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

func titleQuote(goal string) string {
	if r := []rune(goal); len(r) > titleInputRunes {
		return string(r[:titleInputRunes]) + "…"
	}
	return goal
}

func (m *SessionManager) generateTitle(sessionID string, client *provider.ModelClient, fallback, goal string) {
	ctx, cancel := context.WithTimeout(context.Background(), titleTimeout)
	defer cancel()
	req := []provider.Message{titleRequest(titleQuote(goal))}
	resp, err := client.ChatStream(ctx, req, nil, nil, provider.WithMaxTokens(titleOutputTokens))
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
	if len(resp.ToolCalls) > 0 {
		slog.Info("title request answered with a tool call instead of a title", "session", sessionID,
			"tool_calls", len(resp.ToolCalls))
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
