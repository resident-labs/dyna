package watch

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

// mergeNote records root's decision without waking root.
func (s *Service) mergeNote(ctx context.Context, roomID string, args, without []string) error {
	if len(args) != 1 || len(without) != 0 || strings.TrimSpace(args[0]) == "" || strings.ContainsAny(args[0], "\r\n") {
		return fmt.Errorf("merge note requires one nonempty line")
	}
	rm, err := s.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return err
	}
	actor, err := s.Notify.Sessions.AuthorityActor(ctx)
	if err != nil {
		return err
	}
	if err := authority.Check("git merge note", actor, func(relation authority.Relation) bool {
		return relation == authority.RootRoom && (actor.Room == rm.ID || actor.Room == rm.Name)
	}); err != nil {
		return err
	}
	db, err := s.mergeHistoryDB()
	if err != nil {
		return err
	}
	// A decision is a typed merge event: one line, one card, and no wake. Each
	// decision is its own post even when its line repeats.
	post, err := s.Notify.Rooms.PostEventInput(room.PostEventInput{
		RoomID: rm.ID, Event: room.MergeDecisionEvent{Note: args[0]},
		FreshID: true, To: []string{rm.Name}, RoomService: true,
	})
	if err != nil {
		return err
	}
	// Each post is a separate decision, even if its text repeats. Its durable
	// ID also gives the existing history uniqueness rule a stable identity.
	return s.saveMergeHistory(db, MergeHistoryEntry{
		Kind: "decision", Outcome: "recorded", Repo: rm.Repo, Branch: post.ID,
		Changes: []string{}, Detail: post.Text, At: s.now(),
	}, []string{rm.ID})
}
