package engine

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/authority"
)

func (s services) approvalRequest(ctx context.Context, in approvals.RequestInput) (approvals.Request, error) {
	payload, err := json.Marshal(approvals.GHPayload{Argv: in.Argv, Cwd: in.Cwd, Repo: in.Repo, Files: in.Files})
	if err != nil {
		return approvals.Request{}, err
	}
	return s.rooms.Approvals.Create(ctx, authority.Of(ctx), approvals.CreateInput{
		Kind: approvals.KindGH, Class: in.Class, Payload: payload, Repository: in.Repo,
		Command: "gh " + strings.Join(in.Argv, " "), Title: approvalArgument(in.Argv, "--title"), Body: approvalArgument(in.Argv, "--body"),
	})
}

func approvalArgument(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
		if value, ok := strings.CutPrefix(arg, flag+"="); ok {
			return value
		}
	}
	return ""
}

func (s services) approvalList(ctx context.Context, room string) ([]approvals.Summary, error) {
	return s.rooms.Approvals.List(authority.Of(ctx), room)
}

func (s services) approvalDecide(ctx context.Context, in approvals.DecisionInput) (approvals.Request, error) {
	return s.rooms.Approvals.Decide(ctx, authority.Of(ctx), in)
}

func (s services) approvalCancel(ctx context.Context, room, id string) (approvals.Request, error) {
	return s.rooms.Approvals.Cancel(ctx, authority.Of(ctx), room, id)
}

func (s services) approvalWait(ctx context.Context, room, id string, timeout time.Duration) (approvals.Request, error) {
	return s.rooms.Approvals.Wait(ctx, authority.Of(ctx), room, id, timeout)
}

// expireApprovals shares the engine timer set and stops with its context.
// A one-minute sweep is enough for room expiries, which are measured in hours.
func expireApprovals(ctx context.Context, service *approvals.Service, t *timers) {
	wake := make(chan struct{}, 1)
	for {
		if err := service.ExpireDue(ctx); err != nil && ctx.Err() == nil {
			log.Printf("approval expiry: %v", err)
		}
		t.at(timerApprovals, t.now().Add(time.Minute), func() { wakeWorker(wake) })
		select {
		case <-ctx.Done():
			return
		case <-wake:
		}
	}
}

func (s services) approvalSettings(ctx context.Context, in approvals.SettingsInput) (approvals.Settings, error) {
	return s.rooms.Approvals.Settings(authority.Of(ctx), in)
}
