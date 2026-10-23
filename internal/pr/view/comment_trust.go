package view

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"fmt"
	"slices"
	"strings"
)

func (s *Service) AdmitPRComment(ctx context.Context, roomRef, key, id string) error {
	p, err := s.prInRoom(roomRef, key)
	if err != nil {
		return err
	}
	if authority.Of(ctx).Role != authority.Owner {
		return fmt.Errorf("only the owner can admit held PR comments")
	}
	return p.AdmitComment(key, id)
}

func (s *Service) DismissPRComment(ctx context.Context, roomRef, key, id string) error {
	p, err := s.prInRoom(roomRef, key)
	if err != nil {
		return err
	}
	if authority.Of(ctx).Role != authority.Owner {
		return fmt.Errorf("only the owner can dismiss held PR comments")
	}
	return p.DismissComment(key, id)
}

type HeldPRComment struct {
	Key    string `json:"key"`
	RoomID string `json:"roomId"`
	Number int    `json:"number"`
	state.HeldComment
}

func (s *Service) HeldPRComments(ctx context.Context, roomRef string) ([]HeldPRComment, error) {
	if authority.Of(ctx).Role != authority.Owner {
		return nil, fmt.Errorf("only the owner can read held PR comments")
	}
	roomID := ""
	if roomRef != "" {
		rec, err := s.Notify.Rooms.Resolve(roomRef)
		if err != nil {
			return nil, err
		}
		roomID = rec.ID
	}
	st, err := s.Watcher().Store.Read()
	if err != nil {
		return nil, err
	}
	out := []HeldPRComment{}
	for _, r := range st.Records {
		if roomID != "" && r.RoomID != roomID {
			continue
		}
		for _, held := range r.HeldComments {
			out = append(out, HeldPRComment{Key: r.Key, RoomID: r.RoomID, Number: r.Number, HeldComment: held})
		}
	}
	slices.SortFunc(out, func(a, b HeldPRComment) int { return strings.Compare(a.Key+"/"+a.ID, b.Key+"/"+b.ID) })
	return out, nil
}
