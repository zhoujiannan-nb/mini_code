package session


import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/user/mini_code/tools"
)

const (
	goalStoreMetaKey = "goal_store"
	todosMetaKey     = "todos"
)

const goalRoundTag = "<goal_round"

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
	return s.goalStore
}

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

func (s *Session) markGoalRoundOrigin(goal string) {
	s.mu.Lock()
	gs := s.goalStore
	s.mu.Unlock()
	if gs != nil {
		gs.SetAutoRound(strings.HasPrefix(strings.TrimSpace(goal), goalRoundTag))
	}
}

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
		s.appendGoalNotice(fmt.Sprintf(
			`<goal_round status="cap">
Automatic continuation for goal %s stopped: its budget of %d round(s) is spent and the goal is still active. The driver will not start another round. Send a message to keep working it, or update_goal to complete or pause it.
</goal_round>`, g.ID, max))
		return
	}
	if !s.manager.hasDeliverHook() {
		slog.Info("goal round not delivered (no wake path wired)", "session", s.ID(), "goal", g.ID)
		return
	}
	gs.BeginRound()
	s.manager.deliverIdle(s.ID(), goalRoundNotice(g.ID, g.Objective, rounds+1, max))
}

func goalRoundNotice(id, objective string, round, max int) string {
	obj := strings.ReplaceAll(objective, "</goal_round", "")
	return fmt.Sprintf(
		`<goal_round round="%d" max="%d">
Automatic goal continuation for %s (round %d of %d): the session went idle while this goal was still active.
Objective: %s
Keep working the objective now. When it is fully done, call update_goal with action "complete". This is an automatic round: edit/pause/resume are refused here, and blocked only passes after %d delivered rounds.
</goal_round>`, round, max, id, round, max, obj, tools.MinBlockedRounds)
}

func (s *Session) appendGoalNotice(text string) {
	if s.manager == nil {
		return
	}
	if err := s.manager.appendMessage(s.ID(), text); err != nil {
		slog.Warn("goal notice append failed", "session", s.ID(), "err", err)
	}
}
