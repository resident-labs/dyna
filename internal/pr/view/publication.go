package view

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/watch"
)

// publication checks the room before publishing. Ordinary reviewers can publish;
// the admin rule for review on/off does not apply to a review body.
func (s *Service) publication(ctx context.Context, roomRef string, ref watch.Ref, publish bool) (*watch.Service, context.Context, string, error) {
	room, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, ctx, "", err
	}
	_, err = authority.ActorIn(ctx, room.ID)
	if err != nil {
		return nil, ctx, "", err
	}
	p := s.Watcher()
	parsed, err := p.Resolve(ctx, ref.URL(), "")
	if err != nil || parsed.Key() != ref.Key() {
		return nil, ctx, "", fmt.Errorf("invalid PR reference")
	}
	st, err := p.Store.Read()
	if err != nil {
		return nil, ctx, "", err
	}
	if rec := st.Records[ref.Key()]; rec != nil {
		if rec.RoomID != room.ID {
			return nil, ctx, "", fmt.Errorf("PR #%d is not in this room", ref.Number)
		}
	} else if !publish {
		return nil, ctx, "", fmt.Errorf("GitHub publishing needs --publish for an untracked PR: PR is not tracked")
	}
	return p, ghclient.WithRoom(ctx, room.ID), publicationSession(ctx), nil
}

func (s *Service) CommentPR(ctx context.Context, roomRef string, ref watch.Ref, body string, publish bool) error {
	p, ctx, session, err := s.publication(ctx, roomRef, ref, publish)
	if err != nil {
		return err
	}
	return p.Comment(ctx, ref, session, body, publish)
}

func (s *Service) PostPRReview(ctx context.Context, roomRef string, ref watch.Ref, body, head string, comments []watch.ReviewComment, publish bool) (int64, error) {
	p, ctx, session, err := s.publication(ctx, roomRef, ref, publish)
	if err != nil {
		return 0, err
	}
	return p.PostReview(ctx, ref, session, body, head, comments, publish)
}

type LearnedPRNoise struct {
	Rule    watch.NoiseRule `json:"rule"`
	Created bool            `json:"created"`
}

func (s *Service) LearnPRNoise(ctx context.Context, roomRef, key, commentID, reason string) (LearnedPRNoise, error) {
	p, err := s.prInRoom(roomRef, key)
	if err != nil {
		return LearnedPRNoise{}, err
	}
	rec, err := p.Store.Get(key)
	if err != nil {
		return LearnedPRNoise{}, err
	}
	if _, err := authority.ActorIn(ctx, rec.RoomID); err != nil {
		return LearnedPRNoise{}, err
	}
	rule, created, err := p.LearnNoise(ctx, key, commentID, reason, publicationSession(ctx))
	return LearnedPRNoise{Rule: rule, Created: created}, err
}

// publicationSession uses only authenticated identity. Owners and workflow
// workers publish without a session, as they did in the CLI.
func publicationSession(ctx context.Context) string {
	actor := authority.Of(ctx)
	if actor.Role == authority.Owner || actor.Role == authority.Worker {
		return ""
	}
	return actor.Session
}
