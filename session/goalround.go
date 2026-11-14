package session

// goalround.go — the goal-round driver, MiniCode's shape of dsh's
// packages/goal/goal-round-driver.
//
// dsh's rule: a session that goes idle with an ACTIVE goal does not simply
// stop. The host delivers one automatic continuation round after another
// until the goal is completed or blocked, or the round cap is spent. Inside
// an automatic round the model may only keep working or finish: edit/pause/
// resume are human-round-only, and blocked needs MinBlockedRounds delivered
// rounds. The permission half of that lives in tools.GoalStore (the store
// knows the round origin because markGoalRoundOrigin told it before the task
// started); this file is the driving half.
//
// The goal and the todo list live for the life of the CONVERSATION, not the
// task: GoalState/TodoState build them once, restore the last persisted
// snapshot from record metadata, and write every accepted change back
// through the tools' OnChanged hook. The driver itself hangs on Prompt's
// success path after the settle-drain — a human message queued during the
// final turn already re-wakes the session, and that wake is a human round,
// so it wins over the goal round.

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/mini_code/tools"
)

// Metadata keys holding the persisted snapshots (JSON strings, see
// tools.GoalSnapshot / TodoTool.Snapshot).
const (
	goalStoreMetaKey = "goal_store"
	todosMetaKey     = "todos"
)

// goalRoundTag opens the self-describing block the driver delivers. The same
// tag is what NoticeKind uses to label the row as runtime speech (notices.go)
// and what markGoalRoundOrigin uses to decide the round's origin — one
// marker, three readers, dsh's <goal_round> block verbatim in spirit.
const goalRoundTag = "<goal_round"

// GoalState returns the session's goal store: the one goal object every task
// in this conversation shares. Built on first use from the persisted
// snapshot, with the persist hook installed after the restore (a restore
// must not write). Safe from any goroutine.
func (s *Session) GoalState() *tools.GoalStore {
	s.mu.Lock()
	if s.goalStore != nil {
		gs := s.goalStore
		s.mu.Unlock()
		return gs
	}
	saved, _ := s.record.Metadata[goalStoreMetaKey].(string)
	s.mu.Unlock()

	gs := tools.NewGoalStore()
	gs.Restore(saved)
	gs.SetOnChanged(func() {
		snap := gs.Snapshot()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.record.Metadata == nil {
			s.record.Metadata = map[string]interface{}{}
		}
		if snap == "" {
			delete(s.record.Metadata, goalStoreMetaKey)
		} else {
			s.record.Metadata[goalStoreMetaKey] = snap
		}
		s.persistLocked()
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goalStore == nil {
		s.goalStore = gs
	}
	// A concurrent builder may have won the race; its store carries the same
	// restored snapshot and the same hook, so either instance is correct.
	return s.goalStore
}

// TodoState returns the session's task list, the session-lifetime twin of
// GoalState (dsh's todo_write survives across tasks — a long conversation
// must not lose the checklist on task boundaries).
func (s *Session) TodoState() *tools.TodoTool {
	s.mu.Lock()
	if s.sessionTodo != nil {
		tt := s.sessionTodo
		s.mu.Unlock()
		return tt
	}
	saved, _ := s.record.Metadata[todosMetaKey].(string)
	s.mu.Unlock()

	tt := tools.NewTodoTool()
	tt.Restore(saved)
	tt.SetOnChanged(func() {
		snap := tt.Snapshot()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.record.Metadata == nil {
			s.record.Metadata = map[string]interface{}{}
		}
		if snap == "" {
			delete(s.record.Metadata, todosMetaKey)
		} else {
			s.record.Metadata[todosMetaKey] = snap
		}
		s.persistLocked()
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionTodo == nil {
		s.sessionTodo = tt
	}
	return s.sessionTodo
}

// markGoalRoundOrigin records, for the task about to run, whether the driver
// started it (the goal text carries the <goal_round block) or a human did.
// Sessions without a goal surface (children, non-primary modes) have no
// store yet and stay untouched — their update_goal does not exist, so there
// is no rule to gate.
func (s *Session) markGoalRoundOrigin(goal string) {
	s.mu.Lock()
	gs := s.goalStore
	s.mu.Unlock()
	if gs != nil {
		gs.SetAutoRound(strings.HasPrefix(strings.TrimSpace(goal), goalRoundTag))
	}
}

// driveGoalRound runs the driver's decision at the end of a successful task
// that no queued human message re-woke. Called only from Prompt's success
// path (an interrupt or a failure is the human's cue, not the driver's) and
// only for manager-owned sessions.
func (s *Session) driveGoalRound() {
	s.mu.Lock()
	gs := s.goalStore
	s.mu.Unlock()
	if gs == nil {
		return
	}
	g, rounds := gs.View()
	if g == nil || g.Status != "active" {
		return
	}
	max := gs.MaxRounds()
	if rounds >= max {
		// Budget spent. dsh's driver does not swallow this either — the
		// transcript says so — but it does NOT start a task just to be told
		// the cap is hit again: the notice goes into the history append-only,
		// where the human's next message will find it.
		s.appendGoalNotice(fmt.Sprintf(
			`<goal_round status="cap">
Automatic continuation for goal %s stopped: its budget of %d round(s) is spent and the goal is still active. The driver will not start another round. Send a message to keep working it, or update_goal to complete or pause it.
</goal_round>`, g.ID, max))
		return
	}
	if !s.manager.hasDeliverHook() {
		// No wake path wired (CLI run, tests): the round cannot start, so it
		// must not be counted either. Logging beats silently diverging from
		// the persisted rounds counter.
		slog.Info("goal round not delivered (no wake path wired)", "session", s.ID(), "goal", g.ID)
		return
	}
	// Count first, deliver second: BeginRound persists the new count, so a
	// crash mid-round still cannot earn that round twice.
	gs.BeginRound()
	s.manager.deliverIdle(s.ID(), goalRoundNotice(g.ID, g.Objective, rounds+1, max))
}

// goalRoundNotice renders the automatic continuation block. The objective is
// echoed because the round may run after compaction cut the create_goal
// call out of the visible history — dsh's driver carries the objective in
// the round text for the same reason.
func goalRoundNotice(id, objective string, round, max int) string {
	obj := strings.ReplaceAll(objective, "</goal_round", "")
	return fmt.Sprintf(
		`<goal_round round="%d" max="%d">
Automatic goal continuation for %s (round %d of %d): the session went idle while this goal was still active.
Objective: %s
Keep working the objective now. When it is fully done, call update_goal with action "complete". This is an automatic round: edit/pause/resume are refused here, and blocked only passes after %d delivered rounds.
</goal_round>`, round, max, id, round, max, obj, tools.MinBlockedRounds)
}

// appendGoalNotice writes a driver notice into the history without starting
// a task (the cap case). The session must be live in the manager — it is,
// because its task just finished inside it.
func (s *Session) appendGoalNotice(text string) {
	if s.manager == nil {
		return
	}
	if err := s.manager.appendMessage(s.ID(), text); err != nil {
		slog.Warn("goal notice append failed", "session", s.ID(), "err", err)
	}
}
