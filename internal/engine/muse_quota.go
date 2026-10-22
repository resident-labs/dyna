package engine

import (
	"context"
	"errors"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/websvc"
)

func (s services) museQuotaBinding() websvc.Binding {
	return websvc.Binding{Name: "ObserveMuseQuota", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.observeMuseQuota}
}

func (s services) observeMuseQuota(ctx context.Context, in callspec.MuseQuotaObservation) error {
	if err := checkMuseQuotaCaller(ctx, in); err != nil {
		return err
	}
	return s.accounts.ObserveMuseQuota(ctx, in)
}

func checkMuseQuotaCaller(ctx context.Context, in callspec.MuseQuotaObservation) error {
	if in.AdmissionID == "" {
		return errors.New("Muse quota observation needs an admission ID")
	}
	return checkExecutionCaller(ctx, in.ExecutionRef)
}
