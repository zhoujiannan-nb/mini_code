package session

import (
	"github.com/user/mini_code/provider"
	"github.com/user/mini_code/util"
)

func (s *Session) ContextPlan() (parts []PromptPart, defs []provider.ToolSchema, err error) {
	cfg, pb, _, err := s.preparePromptTools()
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	mdl, workDir := s.client, s.record.WorkDir
	s.mu.Unlock()
	if mdl != nil {
		pb.SetModelName(mdl.ModelName())
	}
	return pb.BuildSystemPromptParts(workDir, ""), pb.GetFilteredDefinitions(cfg), nil
}

func (s *Session) CalibratedTokenCount() int {
	all := s.SnapshotMessages()
	raw := util.CountMessagesTokens(all)
	s.mu.Lock()
	mdl := s.client
	s.mu.Unlock()
	if mdl == nil {
		return raw
	}
	_, defs, err := s.ContextPlan()
	if err != nil {
		return raw
	}
	return calibratedEstimator(mdl.ModelName(), defs)(all)
}
