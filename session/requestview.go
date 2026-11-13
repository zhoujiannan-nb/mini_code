package session

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/user/mini_code/provider"
)

// Request-view transforms, learned from the mc-vs-dsh duel (2026-11-13):
// oversized tool arguments and base64 images, re-sent on every turn, made the
// mc side of the duel cost ~6x what dsh cost on the same tasks. Two cheap,
// deterministic, model-free views fix that at ONE choke point (chatWithRetry):
//
//   - stubOldToolArgs: a write/edit call's full-file arguments are the model's
//     own past output; once the conversation has moved on, the bytes are dead
//     weight — replace them with a receipt naming the path and the size. The
//     file is on disk and "read before you write" is already house discipline,
//     so nothing is lost, just not re-carried.
//   - offloadOldImages: a readimg image is vision for the NEXT turns, not an
//     heirloom. Beyond the kept window the base64 is replaced by a text
//     marker that keeps the file path (dsh compaction-image-offload
//     discipline, packages/bundle/base ships dsh-compaction-image-offload).
//
// Both are copy-on-write: the input slice (the live conversation, the DB
// projection, the UI history) is NEVER mutated — only the request view is
// rewritten. Re-computed per request, so the kept window follows the tail of
// the conversation, and idempotent, so chatWithRetry re-sends are safe.
//
// Gated by MC_REQUEST_VIEW=1 (default off): the behaviour must win the
// rematch before it earns the daily driver, and the env makes that A/B free.
const requestViewEnv = "MC_REQUEST_VIEW"

var (
	requestViewOnce sync.Once
	requestViewOn   bool
)

func requestViewEnabled() bool {
	requestViewOnce.Do(func() { requestViewOn = os.Getenv(requestViewEnv) == "1" })
	return requestViewOn
}

// stubArgTools are the tools whose arguments carry whole-file payloads.
// exec/patch-style args stay verbatim: they are small and replay-critical.
var stubArgTools = map[string]bool{"write_file": true, "edit_file": true, "copy_file": true, "move_file": true}

const (
	argStubKeep     = 4    // recent assistant turns whose tool calls stay verbatim
	argStubMinLen   = 2000 // chars below which args are already cheap to re-send
	imageKeepWindow = 2    // recent assistant turns for which images stay in the request
)

// requestView returns the messages to send: possibly stubbed/offloaded, never
// mutating the input. When disabled or when nothing qualifies it returns the
// input slice unchanged.
func requestView(messages []provider.Message) []provider.Message {
	if !requestViewEnabled() || len(messages) == 0 {
		return messages
	}
	out := stubOldToolArgs(messages, argStubKeep, argStubMinLen)
	out = offloadOldImages(out, imageKeepWindow)
	return out
}

// oldBoundary returns the index such that everything BEFORE it sits outside
// the window of the most recent keep assistant turns; -1 when the whole
// conversation is still inside the window.
func oldBoundary(messages []provider.Message, keep int) int {
	seen := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			seen++
			if seen == keep {
				return i
			}
		}
	}
	return -1
}

// stubOldToolArgs replaces oversized write/edit arguments from old turns with
// a receipt. Copy-on-write: only rewritten messages (and their tool-call
// slices) are copied.
func stubOldToolArgs(messages []provider.Message, keep, minLen int) []provider.Message {
	bound := oldBoundary(messages, keep)
	if bound <= 0 {
		return messages
	}
	var out []provider.Message
	for i := 0; i < bound; i++ {
		m := messages[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		var tcs []provider.ToolCall
		for j, tc := range m.ToolCalls {
			if !stubArgTools[tc.Function.Name] || len(tc.Function.Arguments) < minLen {
				continue
			}
			if tcs == nil {
				tcs = make([]provider.ToolCall, len(m.ToolCalls))
				copy(tcs, m.ToolCalls)
			}
			tcs[j].Function.Arguments = toolArgStub(tc.Function.Name, tc.Function.Arguments)
		}
		if tcs != nil {
			if out == nil {
				out = make([]provider.Message, len(messages))
				copy(out, messages)
			}
			out[i].ToolCalls = tcs
		}
	}
	if out == nil {
		return messages
	}
	return out
}

// toolArgStub renders the receipt: the path survives, the payload does not.
// Unparseable arguments still get stubbed — the size itself is the payload.
func toolArgStub(name, args string) string {
	path := ""
	var probe map[string]any
	if err := json.Unmarshal([]byte(args), &probe); err == nil {
		if s, ok := probe["path"].(string); ok {
			path = s
		}
	}
	var b strings.Builder
	b.WriteString("{")
	if path != "" {
		jp, _ := json.Marshal(path)
		fmt.Fprintf(&b, `"path":%s,`, jp)
	}
	fmt.Fprintf(&b, `"content":"[harness view note: this historical %s call SUCCEEDED long ago; its argument text is elided from this request view ONLY. This is NOT the file content and NOT a tool error — the file on disk holds exactly what that call wrote (it may have been edited since). Do NOT re-issue that write because of this note; use read_file on the path to see the current text.]"}`,
		name)
	return b.String()
}

var imageLoadedRe = regexp.MustCompile(`Image loaded from: (.*?) \(`)

// offloadOldImages replaces image parts outside the kept window with a text
// marker that keeps the source path. Copy-on-write like stubOldToolArgs.
func offloadOldImages(messages []provider.Message, keep int) []provider.Message {
	bound := oldBoundary(messages, keep)
	if bound <= 0 {
		return messages
	}
	var out []provider.Message
	for i := 0; i < bound; i++ {
		m := messages[i]
		if len(m.ContentParts) == 0 {
			continue
		}
		hasImage := false
		for _, p := range m.ContentParts {
			if p.Type == "image_url" {
				hasImage = true
				break
			}
		}
		if !hasImage {
			continue
		}
		if out == nil {
			out = make([]provider.Message, len(messages))
			copy(out, messages)
		}
		path := ""
		for _, p := range m.ContentParts {
			if p.Type == "text" {
				if mm := imageLoadedRe.FindStringSubmatch(p.Text); mm != nil {
					path = mm[1]
				}
			}
		}
		parts := make([]provider.ContentPart, 0, len(m.ContentParts))
		for _, p := range m.ContentParts {
			if p.Type != "image_url" {
				parts = append(parts, p)
				continue
			}
			if path != "" {
				parts = append(parts, provider.NewTextPart("[image offloaded: "+path+" — re-run readimg on that file if you need to see it again]"))
			} else {
				parts = append(parts, provider.NewTextPart("[image offloaded from history — re-run readimg if you need to see it again]"))
			}
		}
		out[i].ContentParts = parts
	}
	if out == nil {
		return messages
	}
	return out
}
