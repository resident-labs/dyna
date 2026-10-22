package watch

import (
	"fmt"
	"slices"
)

// RequireReviewers adds canonical session IDs without removing prior requirements.
// The checked author and room bind the write to the record that was authorized.
// It takes the merge publication lock, so a requirement cannot land between a
// batch member's final readiness check and its publication.
func (s *Service) RequireReviewers(key, author, roomID string, reviewers []string) error {
	if len(reviewers) == 0 || len(reviewers) > 20 {
		return fmt.Errorf("require between 1 and 20 reviewers")
	}
	unlock, err := s.mergeLock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.Store.updateRecords([]string{key}, func(st *State) error {
		r := recordByID(st, key)
		if r == nil || !r.Watching || r.State != "open" {
			return fmt.Errorf("required reviews need an open watched change")
		}
		if r.Author != author || r.RoomID != roomID {
			return fmt.Errorf("change author or room changed; authorize the current record")
		}
		next := slices.Clone(r.RequiredReviewers)
		for _, id := range reviewers {
			meta, err := s.sessionStore().ReadMeta(id)
			if err != nil {
				return err
			}
			if meta.ID != id || meta.RoomID != roomID || !meta.Closed.IsZero() || id == author {
				return fmt.Errorf("required reviewer must be an open session in this room other than the author")
			}
			if !slices.Contains(next, id) {
				next = append(next, id)
			}
		}
		if len(next) > 20 {
			return fmt.Errorf("a change can require at most 20 reviewers")
		}
		if slices.Equal(next, r.RequiredReviewers) {
			return errNoStoreChange
		}
		r.RequiredReviewers = next
		return nil
	})
}
