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
	if err := ghclient.ValidateFileSnapshots(in.Argv, in.Files); err != nil {
		return approvals.Request{}, err
	}
	ctx = ghclient.WithRooms(ghclient.WithRoom(ctx, actor.Room), s.rooms.Notify.Rooms)
	var checkedRef ghclient.ItemRef
	var checkedNumber int
	var owns func(context.Context, ghclient.ItemRef) bool
	if actor.Role == authority.Session && actor.Session != "" {
		owns = func(ctx context.Context, ref ghclient.ItemRef) bool {
			number, err := ghclient.ApprovalPRNumber(ctx, in.Cwd, ref)
			if err != nil {
				return false
			}
			records, err := s.rooms.PRs.Watcher().Changes(actor.Room)
			if err != nil {
				return false
			}
			for _, record := range records {
				if record.Author == actor.Session && record.Number == number && strings.EqualFold(record.Host, ref.Host) && strings.EqualFold(record.Repo, ref.Repo) {
					checkedRef, checkedNumber = ref, number
					return true
				}
			}
			return false
		}
	}
	class, pinnedArgs, err := ghclient.ClassifyForSession(ctx, "", in.Cwd, in.Argv, owns)
	if err != nil {
		return approvals.Request{}, err
	}
	if class == approvals.ClassRead {
		return approvals.Request{}, errors.New("read commands do not need approval")
	}
	in.Argv = pinnedArgs
	if class == approvals.ClassAuto && checkedNumber != 0 {
		in.Argv = ghclient.PinRoutinePR(in.Argv, checkedRef, checkedNumber)
	}
	auto := class == approvals.ClassAuto
	if auto {
		class = approvals.ClassAgent
	}
	stored := approvals.GHPayload{Argv: in.Argv, Cwd: in.Cwd, Repo: in.Repo, Files: in.Files}
	payload, err := json.Marshal(stored)
	if err != nil {
		return approvals.Request{}, err
	}
	return s.rooms.Approvals.Create(ctx, actor, approvals.CreateInput{
		Kind: approvals.KindGH, Class: class, Auto: auto, Payload: payload, Repository: in.Repo,
		Command: "gh " + strings.Join(in.Argv, " "), Title: approvalArgument(in.Argv, "--title"), Body: ghclient.DisplayApprovalBody(stored),
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

func (s services) approvalHeld(ctx context.Context, room string) ([]approvals.HeldRequest, error) {
	return s.rooms.Approvals.Held(authority.Of(ctx), room)
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
