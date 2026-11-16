// context_plan.go — the read-only face of preparePromptTools, exported for the
// web layer's「上下文」drawer detail view (GET /api/sessions/{id}/context/detail).
//
// The drawer has to show the two prompt-level pieces that cost tokens but are
// not messages: the system prompt and the tool schemas. Both only exist once a
// PromptBuilder has been assembled for this session, so instead of re-deriving
// them somewhere else (and drifting), the web layer asks the session to render
// exactly what a turn would send — the same single source of truth the agent
// loop's Prompt uses (see preparePromptTools).
package session

import "github.com/user/mini_code/provider"

// ContextPlan renders what a live turn of this session would send above the
// conversation: the system prompt as its sections (PromptPart), and the
// permission-filtered tool definitions.
//
// It is safe to call on an idle session and to call repeatedly: the
// PromptBuilder is a throwaway rebuilt per call, and only the session-owned,
// long-lived pieces (the todo list, the goal store, the shared exec tool) are
// re-attached to it — the same re-attachment a task does, so the tool set the
// drawer shows is the tool set the model sees, not a placeholder list.
//
// A caller that wants the prompt as one string joins the parts with "\n" —
// exactly what BuildSystemPrompt does — so this can never render a different
// prompt than the loop would send.
func (s *Session) ContextPlan() (parts []PromptPart, defs []provider.ToolSchema, err error) {
	cfg, pb, _, err := s.preparePromptTools()
	if err != nil {
		return nil, nil, err
	}
	// Same model naming as Prompt: the persona renders {{model}} from the
	// session's client, so skipping it here would show a system prompt that is
	// not the byte-identical one being billed.
	s.mu.Lock()
	mdl, workDir := s.client, s.record.WorkDir
	s.mu.Unlock()
	if mdl != nil {
		pb.SetModelName(mdl.ModelName())
	}
	return pb.BuildSystemPromptParts(workDir, ""), pb.GetFilteredDefinitions(cfg), nil
}
