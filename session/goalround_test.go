package session

import (
	"context"
	"strings"
	"testing"

	"github.com/user/mini_code/tools"
)


func createActiveGoal(t *testing.T, s *Session, objective string, maxRounds int) {
	t.Helper()
	res, err := tools.NewCreateGoalTool(s.GoalState()).Execute(context.Background(), map[string]interface{}{
		"objective":       objective,
		"max_goal_rounds": float64(maxRounds),
	})
	if err != nil {
		t.Fatalf("create_goal: %v", err)
	}
	if strings.HasPrefix(res.Text, "Error") {
		t.Fatalf("create_goal rejected: %s", res.Text)
	}
}

func TestGoalRoundDeliveredAndCounted(t *testing.T) {
	mgr, rec := newDeliveryEnv(t)
	s, err := mgr.CreateSessionRecord("goal driver", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	createActiveGoal(t, s, "ship the thing", 3)

	s.driveGoalRound()
	got := rec.forSession(s.ID())
	if len(got) != 1 {
		t.Fatalf("delivered %d, want 1 goal round", len(got))
	}
	text := got[0]
	if !strings.HasPrefix(strings.TrimSpace(text), goalRoundTag) {
		t.Fatalf("round notice must carry the tag: %q", text)
	}
	if !strings.Contains(text, `round="1" max="3"`) || !strings.Contains(text, "ship the thing") {
		t.Fatalf("round notice wrong: %q", text)
	}
	if _, rounds := s.GoalState().View(); rounds != 1 {
		t.Fatalf("rounds = %d, want 1", rounds)
	}

	res, err := tools.NewUpdateGoalTool(s.GoalState()).Execute(context.Background(), func() map[string]interface{} {
		g, _ := s.GoalState().View()
		return map[string]interface{}{"goal_id": g.ID, "action": "complete", "revision": float64(g.Revision)}
	}())
	if err != nil || strings.HasPrefix(res.Text, "Error") {
		t.Fatalf("complete: %v %s", err, res.Text)
	}
	s.driveGoalRound()
	if len(rec.forSession(s.ID())) != 1 {
		t.Fatalf("completed goal must not drive: %d deliveries", len(rec.forSession(s.ID())))
	}
}

func TestGoalRoundCapAppendsNoticeWithoutTask(t *testing.T) {
	mgr, rec := newDeliveryEnv(t)
	s, err := mgr.CreateSessionRecord("goal cap", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	createActiveGoal(t, s, "stall forever", 2)

	s.driveGoalRound()
	s.driveGoalRound()
	if n := len(rec.forSession(s.ID())); n != 2 {
		t.Fatalf("delivered %d, want 2 rounds", n)
	}
	s.driveGoalRound()
	if n := len(rec.forSession(s.ID())); n != 2 {
		t.Fatalf("cap must not start a task: %d deliveries", n)
	}
	last := s.record.Messages[len(s.record.Messages)-1].GetText()
	if !strings.Contains(last, `status="cap"`) {
		t.Fatalf("cap notice missing from history: %q", last)
	}
	if kind, ok := NoticeKind(s.record.Messages[len(s.record.Messages)-1]); !ok || kind != NoticeGoal {
		t.Fatalf("cap notice must classify as a goal round: %q %v", kind, ok)
	}
}

func TestGoalRoundsSurviveRestart(t *testing.T) {
	dbPath := t.TempDir() + "/agent.db"
	mgr, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	rec := &deliveryRecorder{}
	mgr.SetDeliverHook(rec.record)
	s, err := mgr.CreateSessionRecord("goal restart", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	createActiveGoal(t, s, "long work", 8)
	s.driveGoalRound()
	if n := len(rec.forSession(s.ID())); n != 1 {
		t.Fatalf("first round not delivered: %d", n)
	}
	sid := s.ID()
	if err := mgr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	mgr2, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer mgr2.Close()
	s2, err := mgr2.Get(sid)
	if err != nil || s2 == nil {
		t.Fatalf("reload: %v", err)
	}
	g, rounds := s2.GoalState().View()
	if g == nil || g.Objective != "long work" || rounds != 1 {
		t.Fatalf("restored goal = %+v rounds=%d, want objective + rounds 1", g, rounds)
	}
}

func TestAutoRoundPermissionsAndOrigin(t *testing.T) {
	mgr, _ := newDeliveryEnv(t)
	s, err := mgr.CreateSessionRecord("goal perms", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	gs := s.GoalState()
	createActiveGoal(t, s, "guarded goal", 8)

	g, _ := gs.View()
	res, _ := tools.NewUpdateGoalTool(gs).Execute(context.Background(), map[string]interface{}{
		"goal_id": g.ID, "action": "edit", "objective": "retargeted", "revision": float64(g.Revision),
	})
	if strings.HasPrefix(res.Text, "Error") {
		t.Fatalf("human edit refused: %s", res.Text)
	}

	s.markGoalRoundOrigin("继续干活")
	s.markGoalRoundOrigin(goalRoundNotice(g.ID, "guarded goal", 2, 8))
	g, _ = gs.View()
	rev := float64(g.Revision)
	res, _ = tools.NewUpdateGoalTool(gs).Execute(context.Background(), map[string]interface{}{
		"goal_id": g.ID, "action": "pause", "revision": rev,
	})
	if !strings.Contains(res.Text, "direct top-level human request") {
		t.Fatalf("pause must fail in an automatic round: %s", res.Text)
	}
	res, _ = tools.NewUpdateGoalTool(gs).Execute(context.Background(), map[string]interface{}{
		"goal_id": g.ID, "action": "blocked", "blocked_reason": "stuck", "revision": rev,
	})
	if !strings.Contains(res.Text, "rejected before 3 automatic rounds") {
		t.Fatalf("early blocked must fail: %s", res.Text)
	}
	gs.BeginRound()
	gs.BeginRound()
	gs.BeginRound()
	g, _ = gs.View()
	res, _ = tools.NewUpdateGoalTool(gs).Execute(context.Background(), map[string]interface{}{
		"goal_id": g.ID, "action": "blocked", "blocked_reason": "stuck", "revision": float64(g.Revision),
	})
	if strings.HasPrefix(res.Text, "Error") {
		t.Fatalf("blocked after the minimum rounds: %s", res.Text)
	}
}

func TestTodoPersistsAcrossTasks(t *testing.T) {
	dbPath := t.TempDir() + "/agent.db"
	mgr, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	s, err := mgr.CreateSessionRecord("todo persist", "build", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	res, err := s.TodoState().Execute(context.Background(), map[string]interface{}{
		"todos": []interface{}{
			map[string]interface{}{"content": "step one", "status": "completed"},
			map[string]interface{}{"content": "step two", "status": "pending"},
		},
	})
	if err != nil || strings.HasPrefix(res.Text, "Error") {
		t.Fatalf("todo_write: %v %s", err, res.Text)
	}
	sid := s.ID()
	if err := mgr.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	mgr2, err := NewSessionManager(nil, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer mgr2.Close()
	s2, err := mgr2.Get(sid)
	if err != nil || s2 == nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s2.TodoState().Snapshot(); !strings.Contains(got, "step one") || !strings.Contains(got, "step two") {
		t.Fatalf("todo list did not survive the restart: %q", got)
	}
}
