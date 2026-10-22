package engine

import (
	"context"
	"errors"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/websvc"
)

func (s services) claudeUsageBinding() websvc.Binding {
	return websvc.Binding{Name: "ObserveClaudeUsage", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.observeClaudeUsage}
}

func (s services) observeClaudeUsage(ctx context.Context, in callspec.ClaudeUsageObservation) error {
	if err := checkClaudeUsageCaller(ctx, in); err != nil {
		return err
	}
	return s.accounts.ObserveClaudeUsage(ctx, in)
}

func checkClaudeUsageCaller(ctx context.Context, in callspec.ClaudeUsageObservation) error {
	if in.AdmissionID == "" {
		return errors.New("Claude usage observation needs an admission ID")
	}
	return checkExecutionCaller(ctx, in.ExecutionRef)
}
