package watch

import (
	"context"
	"fmt"
	"slices"

	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/shellquote"
)

// RequireReviewers adds canonical session IDs without removing prior requirements.
// The checked author and room bind the write to the record that was authorized.
// It takes the reviewer-requirement lock, which a publisher holds only across
// a batch's final readiness check and its publication: a requirement cannot
// land between them, and the write does not wait out whole merge ticks (D-409).
func (s *Service) RequireReviewers(key, author, roomID string, reviewers []string) error {
	if len(reviewers) == 0 || len(reviewers) > 20 {
		return fmt.Errorf("require between 1 and 20 reviewers")
	}
	unlock, err := s.requirementLock(context.Background())
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

// ReleaseReviewers removes required reviewers, so their missing approvals stop
// blocking the change. It works for any requirement, including one whose session
// is closed or gone: closing a session normally releases its own requirement,
// and this is the manual path for every other case. The checked author and room
// bind the write to the record that was authorized. It takes the same
// reviewer-requirement lock as RequireReviewers, so a release cannot land
// between a batch's final readiness check and its publication either.
func (s *Service) ReleaseReviewers(key, author, roomID string, reviewers []string) error {
	if len(reviewers) == 0 || len(reviewers) > 20 {
		return fmt.Errorf("release between 1 and 20 reviewers")
	}
	unlock, err := s.requirementLock(context.Background())
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
		released := false
		for _, id := range reviewers {
			if !slices.Contains(r.RequiredReviewers, id) {
				continue
			}
			r.RequiredReviewers = slices.DeleteFunc(r.RequiredReviewers, func(x string) bool { return x == id })
			r.ReleaseReviewersAfterReplay = slices.DeleteFunc(r.ReleaseReviewersAfterReplay, func(x string) bool { return x == id })
			retireSessionReviewNotices(r, id, "requirement-released")
			released = true
		}
		if !released {
			return errNoStoreChange
		}
		return nil
	})
}

// requiredReviewNotices wakes every required reviewer whose approval the
// current head still needs. A named reviewer records its own verdict, so
// without this re-assignment a dormant reviewer never learns that its older
// verdict stopped counting, and the change stalls until someone wakes the
// reviewer by hand. A draft holds every assignment, like it holds the pool:
// the draft release in the head pass queues them without another push.
func requiredReviewNotices(r *Record) {
	if r.IsDraft {
		return
	}
	command := "dyna git change verdict " + shellquote.Quote(changeID(*r))
	if changeKind(*r) == "github" {
		command = "dyna git pr verdict " + shellquote.Quote(r.URL)
	}
	for _, reviewer := range facts.MissingRequiredReviewers(*r) {
		systemReviewNotice(r, reviewer, fmt.Sprintf("Review %s at its new head %s. Your required approval does not carry over from an older head. Review the diff and record one typed result: %s approve|changes|approve-fast|stop-early --head %s", changeLabel(*r), r.Head, command, r.Head), "required-review:"+r.Head)
	}
}
