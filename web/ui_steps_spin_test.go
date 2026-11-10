package web

import (
	"strings"
	"testing"
)

// TestToolStepSpinner pins the running-tool-step UI: the same compact
// one-line collapsible format as the thinking card, a rotating spinner for
// every state that is still waiting (执行中 / 待返回 / 生成参数中), a live
// "执行 xxs" counter while the call runs, and "总共 xxs" on the receipt.
// A breathing dot does not read as "alive"; a ring with a gap in it does —
// and a finished step must never spin.
func TestToolStepSpinner(t *testing.T) {
	js := jsModulesForTest(t)

	tag := jsFunc(t, js, "stepsTag")
	if !strings.Contains(tag, `class="spin"`) {
		t.Errorf("stepsTag must attach the spinner:\n%s", tag)
	}
	for _, kind := range []string{"run", "soft", "prep"} {
		if !strings.Contains(tag, `"`+kind+`"`) {
			t.Errorf("stepsTag spinner must cover the waiting kind %q:\n%s", kind, tag)
		}
	}
	// ok/dim are terminal: no spinner for them (the spin line lists only the
	// three waiting kinds).
	if strings.Contains(tag, `"ok"`) || strings.Contains(tag, `"dim"`) {
		t.Errorf("terminal tags must not spin:\n%s", tag)
	}

	tick := jsFunc(t, js, "stepsRunTick")
	if !strings.Contains(tick, `"执行 "`) || strings.Contains(tick, "已运行") {
		t.Errorf("the live counter must read 执行 xxs:\n%s", tick)
	}
	if !strings.Contains(tick, `class="spin"`) {
		t.Errorf("the 正在执行 header must spin:\n%s", tick)
	}

	fill := jsFunc(t, js, "stepsFill")
	if !strings.Contains(fill, `"✓ " + (dur >= 2 ? "总共 "`) {
		t.Errorf("the receipt badge must report 总共 xxs:\n%s", fill)
	}

	add := jsFunc(t, js, "stepsAddTool")
	if !strings.Contains(add, `"run", "执行中"`) {
		t.Errorf("a live tool step must be tagged 执行中:\n%s", add)
	}
	// A refreshed「待返回」step only spins while the session is actually
	// running — a dead task must not fake a spinner.
	if !strings.Contains(add, `"(历史)"`) || !strings.Contains(add, "state.running") || !strings.Contains(add, `"soft"`) {
		t.Errorf("history 待返回 must spin only when the session is running:\n%s", add)
	}

	// The resync upgrades still-waiting steps to the spinner once the ledger
	// confirms the task is live.
	resync := jsFunc(t, js, "resyncRunning")
	if !strings.Contains(resync, `stepsTag(st, "soft", "待返回")`) || !strings.Contains(resync, "data.active") {
		t.Errorf("resync must upgrade still-waiting steps to the spinner when active:\n%s", resync)
	}

	// The spinner itself: a small ring in the badge's own color, reusing the
	// existing spin keyframes.
	var css strings.Builder
	for _, p := range embeddedUIAssets(t, ".css") {
		if strings.HasSuffix(p, "base.css") {
			css.Write(uiAssetBody(t, p))
		}
	}
	if !strings.Contains(css.String(), ".spin {") || !strings.Contains(css.String(), "animation: spin") {
		t.Error("base.css must define the .spin ring on the shared spin keyframes")
	}
}
