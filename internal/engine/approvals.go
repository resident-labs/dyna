package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
)

const githubWrite approvals.OperationKind = "github.write"

func (s services) approvalRequest(ctx context.Context, in approvals.RequestInput) (approvals.Request, error) {
	actor := authority.Of(ctx)
	if err := ghclient.ValidateFileSnapshots(in.Argv, in.Files); err != nil {
		return approvals.Request{}, err
	}
	paths, err := ghclient.MediaPaths(in.Argv)
	if err != nil {
		return approvals.Request{}, err
	}
	if len(paths) != len(in.Media) {
		return approvals.Request{}, errors.New("media arguments do not match attachments")
	}
	for i := range in.Media {
		media := &in.Media[i]
		if media.Path != paths[i] || media.Name != filepath.Base(media.Path) || media.PostID != "" || media.AttachmentID != "" {
			return approvals.Request{}, errors.New("invalid media")
		}
		sum := sha256.Sum256(media.Data)
		if fmt.Sprintf("%x", sum) != media.SHA256 {
			return approvals.Request{}, errors.New("media hash mismatch")
		}
		data, err := ghclient.SanitizeMedia(media.Name, media.Data)
		if err != nil {
			return approvals.Request{}, err
		}
		media.Data = data
		sum = sha256.Sum256(data)
		media.SHA256 = fmt.Sprintf("%x", sum)
	}
	payload := &approvals.GHPayload{Argv: in.Argv, Cwd: in.Cwd, Repo: in.Repo, Files: in.Files, Media: in.Media}
	out, err := approvals.Submit(ctx, s.rooms.Approvals, actor, githubWrite, payload)
	if err != nil {
		return approvals.Request{}, err
	}
	if out.Result != nil {
		state := approvals.StateDone
		if out.Result.ExitCode != nil && *out.Result.ExitCode != 0 {
			state = approvals.StateFailed
		}
		return approvals.Request{State: state, Result: out.Result}, nil
	}
	return out.Request, nil
}

func (s services) classifyGitHubWrite(ctx context.Context, actor authority.Actor, in *approvals.GHPayload) (approvals.Class, error) {
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
		var refusal *ghclient.RefusalError
		if errors.As(err, &refusal) && actor.Session != "" {
			rec, resolveErr := s.rooms.Notify.Rooms.Resolve(actor.Room)
			if resolveErr != nil {
				return approvals.Class(""), resolveErr
			}
			sender, senderErr := s.rooms.Notify.SenderOf(notify.Claim{Session: actor.Session, Room: actor.Room})
			if senderErr != nil {
				return approvals.Class(""), senderErr
			}
			event := room.GitHubWriteRefusedEvent{Requester: sender.Session.Address(), Verb: room.GitHubWriteVerb(refusal.Write.Verb), Repository: refusal.Write.Host + "/" + refusal.Write.Repo, Reason: room.GitHubWriteReason(refusal.Decision.Refusal), Rule: room.GitHubWriteRule(refusal.Decision.Rule)}
			if _, postErr := s.rooms.Notify.Rooms.PostEventInput(room.PostEventInput{RoomID: actor.Room, Event: event, FreshID: true, Producer: room.ProducerEngine, To: []string{rec.Name}}); postErr != nil {
				return approvals.Class(""), postErr
			}
		}
		return approvals.Class(""), err
	}
	if class == approvals.ClassRead {
		return approvals.Class(""), errors.New("read commands do not need approval")
	}
	in.Argv = pinnedArgs
	if class == approvals.ClassAuto && checkedNumber != 0 {
		in.Argv = ghclient.PinRoutinePR(in.Argv, checkedRef, checkedNumber)
	}
	return class, nil
}

func (s services) registerGitHubWrite() {
	approvals.Register(s.rooms.Approvals, approvals.Operation[*approvals.GHPayload]{
		Kind: githubWrite,
		Decide: func(ctx context.Context, actor authority.Actor, p *approvals.GHPayload) (approvals.Decision, error) {
			class, err := s.classifyGitHubWrite(ctx, actor, p)
			if err == nil && class == approvals.ClassAuto && len(p.Media) > 0 {
				class = approvals.ClassAgent
			}
			if err != nil {
				return approvals.Decision{}, err
			}
			if len(p.Media) > 0 {
				c := ghclient.WithRooms(ghclient.WithRoom(ctx, actor.Room), s.rooms.Notify.Rooms)
				p.MediaDestination, err = ghclient.ResolveMediaDestination(c, ghclient.Command(c, "", p.Cwd, p.Argv...))
				if err != nil {
					return approvals.Decision{}, err
				}
			}
			p.ApprovedClass = class
			return approvals.Decision{Class: class}, nil
		},
		Card: func(p *approvals.GHPayload) approvals.Card {
			body := ghclient.DisplayApprovalBody(*p)
			d := p.MediaDestination
			if len(p.Media) > 0 {
				body += fmt.Sprintf("\n\nMedia destination: %s/%s (head %s, %s visibility)", d.Host, d.Base, d.Head, d.Visibility)
				if d.Number > 0 {
					body += fmt.Sprintf(" PR #%d", d.Number)
				}
			}
			for _, media := range p.Media {
				body += fmt.Sprintf("\n\nMedia: %s\nSHA-256: %s", media.Name, media.SHA256)
			}
			return approvals.Card{Repository: p.Repo, Command: "gh " + strings.Join(p.Argv, " "), Title: approvalArgument(p.Argv, "--title"), Body: body}
		},
		PrepareHeld: func(ctx context.Context, actor authority.Actor, p *approvals.GHPayload) error {
			if len(p.Media) == 0 {
				return nil
			}
			sender, err := s.rooms.Notify.SenderOf(notify.Claim{Session: actor.Session, Room: actor.Room})
			if err != nil {
				return err
			}
			for i := range p.Media {
				m := &p.Media[i]
				result, err := s.rooms.Notify.Post(ctx, notify.PostInput{NoWake: true, Sender: sender, Mentions: []string{"@root"}, Text: "Media for GitHub approval. Image metadata is removed; video metadata is not removed.", Files: []notify.File{{Name: m.Name, Data: m.Data}}})
				if err != nil {
					return err
				}
				if len(result.Message.Attachments) != 1 {
					return errors.New("sanitized media post failed")
				}
				m.PostID, m.AttachmentID, m.Data = result.Message.ID, result.Message.Attachments[0].ID, nil
			}
			return nil
		},
		Check: func(ctx context.Context, actor authority.Actor, p *approvals.GHPayload) error {
			class, err := s.classifyGitHubWrite(ctx, actor, p)
			if err != nil {
				return err
			}
			if class == approvals.ClassOwner && p.ApprovedClass != approvals.ClassOwner {
				return errors.New("GitHub write now requires owner approval")
			}
			if class == approvals.ClassAgent && p.ApprovedClass == approvals.ClassAuto && actor.Role == authority.Session {
				return errors.New("GitHub write now requires agent approval")
			}
			return nil
		},
		Execute: func(ctx context.Context, actor authority.Actor, p *approvals.GHPayload) (approvals.Result, error) {
			raw, err := json.Marshal(p)
			if err != nil {
				return approvals.Result{}, err
			}
			return ghApprovalExecutor(s.rooms.Notify.Rooms, s.rooms.PRs.Watcher().ClaimIssueComment)(ctx, approvals.Request{Room: actor.Room, Requester: actor.Session, Class: p.ApprovedClass, Payload: raw})
		},
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
