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

// ObserveClaudeUsage consumes an execution reference already checked by the
// engine. It resolves the immutable account binding rather than active state.
func (s *Service) ObserveClaudeUsage(ctx context.Context, in callspec.ClaudeUsageObservation) error {
	if s.o.Store == nil || s.o.Store.Root == "" {
		return errors.New("saved account storage is not configured")
	}
	if in.Identity == "" || in.ObservedAt.IsZero() || len(in.Windows) == 0 {
		return errors.New("Claude usage observation needs a matching identity, an observation time and windows")
	}
	for _, w := range in.Windows {
		if w.ResetsAt.IsZero() || w.UsedPercent < 0 || w.UsedPercent > 100 {
			return errors.New("Claude usage observation has an invalid window")
		}
	}
	fact, err := attempts.Load(ctx, filepath.Dir(s.o.Store.Root), in.ExecutionRef)
	if err != nil {
		return err
	}
	if fact.Harness != "claude-code" || fact.Identity != in.Identity {
		return errors.New("Claude usage observation differs from the admitted account")
	}
	return s.o.Store.UpdateContext(ctx, func(st *store.State) error {
		entry := st.Find(fact.AccountID)
		if entry == nil || entry.Harness != fact.Harness || entry.Identity != in.Identity {
			return errors.New("saved account changed after native admission")
		}
		applyClaudeUsageObservation(entry, in)
		return nil
	})
}

// applyClaudeUsageObservation merges the in-stream windows into the saved
// reading. A newer probe still wins; independent windows and the saved plan
// are preserved. It changes observation metadata only, never credentials.
func applyClaudeUsageObservation(entry *store.Entry, in callspec.ClaudeUsageObservation) {
	if entry.Observation == nil {
		entry.Observation = &store.Observation{}
	}
	observation := entry.Observation
	if observation.ObservedAt.After(in.ObservedAt) {
		return
	}
	observation.ObservedAt = in.ObservedAt
	// A live in-stream reading is as good as a probe attempt, so the next
	// background probe waits for its interval.
	observation.LastAttemptAt = in.ObservedAt
	observation.Unavailable = false
	observation.UsageError = ""
	for _, w := range in.Windows {
		label := "Weekly (7-day)"
		if w.ID == "five_hour" {
			label = "Session (5-hour)"
		}
		percent := w.UsedPercent
		window := store.Window{ID: w.ID, Label: label, UsedPercent: &percent, Unit: "percent", ResetsAt: w.ResetsAt.UTC().Format(time.RFC3339Nano)}
		replaced := false
		for i := range observation.Windows {
			if observation.Windows[i].ID == w.ID {
				observation.Windows[i] = window
				replaced = true
				break
			}
		}
		if !replaced {
			observation.Windows = append(observation.Windows, window)
		}
	}
}
