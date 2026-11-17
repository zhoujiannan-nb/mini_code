package web

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestUIRendersReasoningInABox(t *testing.T) {
	assets, err := loadUIAssets()
	if err != nil {
		t.Fatalf("loadUIAssets: %v", err)
	}

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

	js := jsModulesForTest(t)
	if n := strings.Count(js, `from "./ThinkBox.js"`); n < 2 {
		t.Errorf(`only %d module(s) import "./ThinkBox.js"; Message.js and Steps.js both must`, n)
	}
	if strings.Contains(jsCodeOnly(js), `<details class="m-think"`) {
		t.Error(`a render path still draws the old <details class="m-think"> collapse`)
	}

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

	for _, r := range rules {
		if !strings.Contains(r.sel, "think-box") {
			continue
		}
		if strings.Contains(r.sel, ".collapsed") {
			continue
		}
		if strings.Contains(strings.ReplaceAll(r.body, " ", ""), "display:none") {
			t.Errorf("the rule for %q hides the reasoning box: %s", r.sel, strings.TrimSpace(r.body))
		}
	}
}

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
	return stripCSSComments(b.String())
}

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
