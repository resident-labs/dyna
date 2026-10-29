package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
)

func (s services) approvalRequest(ctx context.Context, in approvals.RequestInput) (approvals.Request, error) {
	actor := authority.Of(ctx)
	rec, err := s.rooms.Notify.Rooms.Resolve(actor.Room)
	if err != nil {
		return approvals.Request{}, err
	}
	stored := ghclient.AttributeApproval(approvals.GHPayload{Argv: in.Argv, Cwd: in.Cwd, Repo: in.Repo, Files: in.Files}, rec.Attribution)
	if err := ghclient.ValidateFileSnapshots(stored.Argv, stored.Files); err != nil {
		return approvals.Request{}, err
	}
	ctx = ghclient.WithRooms(ghclient.WithRoom(ctx, actor.Room), s.rooms.Notify.Rooms)
	class, err := ghclient.ClassifyForApproval(ctx, "", stored.Cwd, stored.Argv)
	if err != nil {
		return approvals.Request{}, err
	}
	if class == approvals.ClassRead {
		return approvals.Request{}, errors.New("read commands do not need approval")
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return approvals.Request{}, err
	}
	return s.rooms.Approvals.Create(ctx, actor, approvals.CreateInput{
		Kind: approvals.KindGH, Class: class, Payload: payload, Repository: stored.Repo,
		Command: "gh " + strings.Join(stored.Argv, " "), Title: approvalArgument(stored.Argv, "--title"), Body: ghclient.DisplayApprovalBody(stored),
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
