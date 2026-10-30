package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
)

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
	if len(paths) > 0 {
		if (actor.Role != authority.Session && actor.Role != authority.Root) || actor.Session == "" {
			return approvals.Request{}, errors.New("media approval requires a session")
		}
		sender, err := s.rooms.Notify.SenderOf(notify.Claim{Session: actor.Session, Room: actor.Room})
		if err != nil {
			return approvals.Request{}, err
		}
		for i, media := range in.Media {
			if media.Path != paths[i] || media.Name != filepath.Base(media.Path) || media.PostID == "" || media.AttachmentID == "" {
				return approvals.Request{}, errors.New("invalid media reference")
			}
			msg, err := s.rooms.Notify.Rooms.Message(actor.Room, media.PostID)
			if err != nil {
				return approvals.Request{}, err
			}
			if msg.AuthorSessionID() != actor.Session {
				return approvals.Request{}, errors.New("media post belongs to another session")
			}
			found := false
			for _, a := range msg.Attachments {
				if a.ID == media.AttachmentID && a.Name == media.Name {
					found = true
				}
			}
			if !found {
				return approvals.Request{}, errors.New("media attachment is not in its post")
			}
			path, err := s.rooms.Notify.Rooms.AttachmentPath(actor.Room, media.AttachmentID)
			if err != nil {
				return approvals.Request{}, err
			}
			f, err := os.Open(path)
			if err != nil {
				return approvals.Request{}, err
			}
			data, err := io.ReadAll(io.LimitReader(f, (25<<20)+1))
			closeErr := f.Close()
			if err != nil {
				return approvals.Request{}, err
			}
			if closeErr != nil {
				return approvals.Request{}, closeErr
			}
			sum := sha256.Sum256(data)
			if fmt.Sprintf("%x", sum) != media.SHA256 {
				return approvals.Request{}, errors.New("media hash mismatch")
			}
			data, err = ghclient.SanitizeMedia(media.Name, data)
			if err != nil {
				return approvals.Request{}, err
			}
			result, err := s.rooms.Notify.Post(ctx, notify.PostInput{NoWake: true, Sender: sender, Mentions: []string{"@root"}, Text: "Media for GitHub approval. Image metadata is removed; video metadata is not removed.", Files: []notify.File{{Name: media.Name, Data: data}}})
			if err != nil {
				return approvals.Request{}, err
			}
			if len(result.Message.Attachments) != 1 {
				return approvals.Request{}, errors.New("sanitized media post failed")
			}
			sum = sha256.Sum256(data)
			in.Media[i].PostID = result.Message.ID
			in.Media[i].AttachmentID = result.Message.Attachments[0].ID
			in.Media[i].SHA256 = fmt.Sprintf("%x", sum)
		}
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
	auto := class == approvals.ClassAuto && len(in.Media) == 0
	if class == approvals.ClassAuto {
		class = approvals.ClassAgent
	}
	var destination approvals.MediaDestination
	if len(in.Media) > 0 {
		cmd := ghclient.Command(ctx, "", in.Cwd, in.Argv...)
		destination, err = ghclient.ResolveMediaDestination(ctx, cmd)
		if err != nil {
			return approvals.Request{}, err
		}
	}
	stored := approvals.GHPayload{MediaDestination: destination, Argv: in.Argv, Cwd: in.Cwd, Repo: in.Repo, Files: in.Files, Media: in.Media}
	body := ghclient.DisplayApprovalBody(stored)
	if len(in.Media) > 0 {
		body += fmt.Sprintf("\n\nMedia destination: %s/%s (head %s, %s visibility)", destination.Host, destination.Base, destination.Head, destination.Visibility)
		if destination.Number > 0 {
			body += fmt.Sprintf(" PR #%d", destination.Number)
		}
	}
	for _, media := range in.Media {
		body += fmt.Sprintf("\n\nMedia: %s\nSHA-256: %s", media.Name, media.SHA256)
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return approvals.Request{}, err
	}
	return s.rooms.Approvals.Create(ctx, actor, approvals.CreateInput{
		Kind: approvals.KindGH, Class: class, Auto: auto, RequireReview: len(in.Media) > 0, Payload: payload, Repository: in.Repo,
		Command: "gh " + strings.Join(in.Argv, " "), Title: approvalArgument(in.Argv, "--title"), Body: body,
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
