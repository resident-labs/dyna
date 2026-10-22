package service

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"dyna-agent/internal/account/attempts"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/callspec"
)

// ObserveMuseQuota consumes an execution reference already checked by the
// engine. It resolves the immutable account binding rather than active state.
func (s *Service) ObserveMuseQuota(ctx context.Context, in callspec.MuseQuotaObservation) error {
	if s.o.Store == nil || s.o.Store.Root == "" {
		return errors.New("saved account storage is not configured")
	}
	if in.Identity == "" || in.ObservedAt.IsZero() || !in.ResetsAt.After(in.ObservedAt) {
		return errors.New("Muse quota observation needs a matching identity and valid observation/reset times")
	}
	fact, err := attempts.Load(ctx, filepath.Dir(s.o.Store.Root), in.ExecutionRef)
	if err != nil {
		return err
	}
	if fact.Harness != "muse" || fact.Identity != in.Identity {
		return errors.New("Muse quota observation differs from the admitted account")
	}
	return s.o.Store.UpdateContext(ctx, func(st *store.State) error {
		entry := st.Find(fact.AccountID)
		if entry == nil || entry.Harness != fact.Harness || entry.Identity != fact.Identity {
			return errors.New("saved account changed after native admission")
		}
		applyMuseQuotaObservation(entry, in.ResetsAt, in.ObservedAt)
		return nil
	})
}

// applyMuseQuotaObservation preserves independent windows and the saved plan.
// It changes observation metadata only, never the account's credentials.
func applyMuseQuotaObservation(entry *store.Entry, reset, now time.Time) {
	if entry.Observation == nil {
		entry.Observation = &store.Observation{}
	}
	observation := entry.Observation
	if observation.ObservedAt.After(now) {
		return
	}
	used := 100.0
	window := store.Window{ID: "subscription", Label: "Subscription (last observed)", UsedPercent: &used, Unit: "percent", ResetsAt: reset.Format(time.RFC3339Nano)}
	windows := []store.Window{}
	for _, previous := range observation.Windows {
		if previous.ID != window.ID {
			windows = append(windows, previous)
		}
	}
	observation.Windows = append(windows, window)
	observation.ObservedAt = now
	observation.RateLimitedUntil = &reset
	observation.Unavailable = false
}
