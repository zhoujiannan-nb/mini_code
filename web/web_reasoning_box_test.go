package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestUIRendersReasoningInABox pins the model-thinking display contract.
//
// reasoning_content reaches the browser on its own (channels emits
// reasoning_delta + the assistant message carries Reasoning, and the session
// detail serves reasoning_content), so the only thing that ever stood between
// that and the screen was the markup: history messages and tool steps each hid
// it behind a <details> collapse and the live one carried .m-think-live
// {display:none}. The UI ships one .think-box for all three paths; since 2026-11
// it is collapsible by design (live expanded, the rest folded by the component,
// body still in the DOM under .collapsed) — this test fails if the box ever
// disappears from a render path again or loses its cap/scroll.
func TestUIRendersReasoningInABox(t *testing.T) {
	assets, err := loadUIAssets()
	if err != nil {
		t.Fatalf("loadUIAssets: %v", err)
	}

	// 1) The box component ships and is served to the browser.
	think := assets["/js/components/ThinkBox.js"]
	if think == nil {
		t.Fatal("the UI does not ship /js/components/ThinkBox.js — reasoning has nowhere to render")
	}
	component := jsCodeOnly(string(think.body))
	if !strings.Contains(component, "think-box-body") {
		t.Error("ThinkBox.js does not render a .think-box-body element")
	}
	if strings.Contains(component, "<details") {
		t.Error("the reasoning box is a <details> collapse again — it must be visible without a click")
	}

	// 2) Both render paths (the message card and the steps card) draw it.
	js := jsModulesForTest(t)
	if n := strings.Count(js, `from "./ThinkBox.js"`); n < 2 {
		t.Errorf(`only %d module(s) import "./ThinkBox.js"; Message.js and Steps.js both must`, n)
	}
	if strings.Contains(jsCodeOnly(js), `<details class="m-think"`) {
		t.Error(`a render path still draws the old <details class="m-think"> collapse`)
	}

	// 3) The stylesheet caps the box and lets it scroll inside itself.
	rules := cssRulesForTest(uiStylesForTest(t))
	capped, scrollable := false, false
	for _, r := range rules {
		if !strings.Contains(r.sel, ".think-box-body") {
			continue
		}
		if strings.Contains(r.body, "overflow-y: auto") {
			scrollable = true
		}
		if m := maxHeightEm.FindStringSubmatch(r.body); m != nil {
			if em := parseFloat(t, m[1]); em >= 6 && em <= 14 {
				capped = true
			} else {
				t.Errorf(".think-box-body max-height = %s em, want roughly 8-12 em", m[1])
			}
		}
	}
	if !capped {
		t.Error(`no ".think-box-body" rule caps the box with a max-height in em`)
	}
	if !scrollable {
		t.Error(`no ".think-box-body" rule sets overflow-y: auto — long thinking would push the page away`)
	}

	// 4) Nothing unconditionally hides the thinking box anywhere in the shipped
	// CSS. (The composer's .think-pop popover is legitimately display:none until
	// opened, and the component's own .collapsed fold is the 2026-11 design:
	// live expanded, older boxes folded by class, click to open.)
	for _, r := range rules {
		if !strings.Contains(r.sel, "think-box") {
			continue
		}
		if strings.Contains(r.sel, ".collapsed") {
			continue // component-driven fold, toggled by state — not a hide-by-markup
		}
		if strings.Contains(strings.ReplaceAll(r.body, " ", ""), "display:none") {
			t.Errorf("the rule for %q hides the reasoning box: %s", r.sel, strings.TrimSpace(r.body))
		}
	}
}

// jsCodeOnly strips comments from ES module source, so a test that asks "does
// this code draw a <details>?" is not answered by a comment about one.
func jsCodeOnly(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return stripCSSComments(b.String()) // /* */ comments use the same markers here
}

// uiStylesForTest returns every embedded stylesheet, concatenated.
func uiStylesForTest(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range embeddedUIAssets(t, ".css") {
		b.Write(uiAssetBody(t, p))
		b.WriteString("\n")
	}
	return b.String()
}

type cssRule struct{ sel, body string }

// cssRulesForTest splits a stylesheet into selector/declaration pairs. CSS
// comments are stripped first (this project annotates inside rule bodies), and
// nested blocks (@media, @keyframes) are flattened to their inner rules — that
// is all this test needs to ask "which rule would win for this class".
func cssRulesForTest(src string) []cssRule {
	src = stripCSSComments(src)
	var out []cssRule
	for i := 0; i < len(src); {
		open := strings.IndexByte(src[i:], '{')
		if open < 0 {
			return out
		}
		sel := strings.TrimSpace(src[i : i+open])
		depth, k := 1, i+open+1
		for k < len(src) && depth > 0 {
			switch src[k] {
			case '{':
				depth++
			case '}':
				depth--
			}
			k++
		}
		body := src[i+open+1 : k-1]
		if strings.Contains(body, "{") {
			out = append(out, cssRulesForTest(body)...)
		} else {
			out = append(out, cssRule{sel: sel, body: body})
		}
		i = k
	}
	return out
}

func stripCSSComments(src string) string {
	for {
		a := strings.Index(src, "/*")
		if a < 0 {
			return src
		}
		b := strings.Index(src[a+2:], "*/")
		if b < 0 {
			return src[:a]
		}
		src = src[:a] + src[a+2+b+2:]
	}
}

var maxHeightEm = regexp.MustCompile(`max-height:\s*([0-9.]+)em`)

func parseFloat(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return f
}
