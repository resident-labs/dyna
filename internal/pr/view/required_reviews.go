package view

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/runstore"
)

// RequirePRReviewers uses the existing author/ancestor/root permission check and
// resolves display names once. Requirements and verdicts use exact session IDs.
func (s *Service) RequirePRReviewers(ctx context.Context, roomRef, key string, reviewers []string) error {
	p, r, err := s.authorPRInRoom(ctx, "git pr require", roomRef, key)
	if err != nil {
		return err
	}
	if len(reviewers) == 0 || len(reviewers) > 20 {
		return fmt.Errorf("require between 1 and 20 reviewers")
	}
	rm, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(reviewers))
	for _, ref := range reviewers {
		ref = strings.TrimPrefix(strings.TrimSpace(ref), "@")
		if ref == "" {
			return fmt.Errorf("a reviewer name or session ID is required")
		}
		if runstore.IsSessionDirID(ref) {
			ids = append(ids, ref)
			continue
		}
		if !strings.Contains(ref, ":") {
			ref = rm.Name + ":" + ref
		}
		info, err := s.Notify.Sessions.Store.ResolveSnapshot(ref)
		if err != nil {
			return err
		}
		ids = append(ids, info.ID)
	}
	return p.RequireReviewers(r.Key, r.Author, r.RoomID, ids)
}

// ReleasePRReviewers uses the same author/ancestor/root permission check as
// requiring reviewers. A closed reviewer resolves only by session ID, which
// the change view shows, so a requirement can be cleared even when its
// session is gone.
func (s *Service) ReleasePRReviewers(ctx context.Context, roomRef, key string, reviewers []string) error {
	p, r, err := s.authorPRInRoom(ctx, "git pr unrequire", roomRef, key)
	if err != nil {
		return err
	}
	if len(reviewers) == 0 || len(reviewers) > 20 {
		return fmt.Errorf("release between 1 and 20 reviewers")
	}
	rm, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(reviewers))
	for _, ref := range reviewers {
		ref = strings.TrimPrefix(strings.TrimSpace(ref), "@")
		if ref == "" {
			return fmt.Errorf("a reviewer name or session ID is required")
		}
		if runstore.IsSessionDirID(ref) {
			ids = append(ids, ref)
			continue
		}
		if !strings.Contains(ref, ":") {
			ref = rm.Name + ":" + ref
		}
		info, err := s.Notify.Sessions.Store.ResolveSnapshot(ref)
		if err != nil {
			return err
		}
		ids = append(ids, info.ID)
	}
	return p.ReleaseReviewers(r.Key, r.Author, r.RoomID, ids)
}
