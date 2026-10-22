package engine

import (
	"context"
	"fmt"
	"log"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/subagent"
)

// configureTurnReports keeps every report on the engine worker context. A
// caller can leave after scheduling; the pending row remains until notify
// stores its post or deliberate silent result.
func configureTurnReports(ctx context.Context, service *notify.Service) {
	manager := service.Sessions
	manager.TurnReporter = func(_ context.Context, _ *subagent.Manager, id string, turn int) error {
		info, err := manager.Store.Inspect(id)
		if err != nil {
			return err
		}
		// Check before reserving the worker key. An exit wake can then
		// schedule this pending row even if the runner exits during this check.
		if info.CurrentTurn == turn {
			return nil
		}
		manager.Workers.Go(fmt.Sprintf("completion %s/%d", id, turn), func(ctx context.Context) error {
			_, err := service.CompleteSessionTurn(ctx, id, turn)
			return err
		})
		return nil
	}
	manager.Store.ReportRepaired = func(store *subagent.Store, id string, turn int) {
		if err := store.PendReport(ctx, id, turn); err != nil {
			log.Printf("repaired turn completion %s/%d: %v", id, turn, err)
			return
		}
		_ = manager.TurnReporter(ctx, manager, id, turn)
	}
}

// resumeTurnReports queries indexed pending rows after spool recovery. It
// never walks session folders or replays historical Reported turns.
func resumeTurnReports(ctx context.Context, service *notify.Service, sessionID string) error {
	pending, err := service.Sessions.Store.PendingReports(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, report := range pending {
		if err := service.Sessions.TurnReporter(ctx, service.Sessions, report.SessionID, report.Turn); err != nil {
			return err
		}
	}
	return nil
}
