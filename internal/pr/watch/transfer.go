package watch

import (
	"context"
	"fmt"
	"path/filepath"

	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/subagent"
)

// Transfer moves one live PR role and preserves the previous author's markers
// or the previous review's provenance. Its caller checks who may transfer. dir
// is the caller's checkout of the PR's repository, or "".
func (s *Service) Transfer(ctx context.Context, key, role string, to subagent.Info, dir string) error {
	if role != "author" {
		return fmt.Errorf("only author transfers are supported; use dyna git pr reviewer --profile or --restart for the managed reviewer")
	}
	// git runs here, never under the PR lock.
	var path string
	if r, err := s.Store.Get(key); err == nil {
		path = matchingCheckout(ctx, r, dir)
	}
	err := s.Store.update(func(st *State) error {
		r := st.Records[key]
		if r == nil || !r.Watching || r.State != "open" {
			return fmt.Errorf("PR is not open and tracked")
		}
		if to.ID == "" || to.RoomID != r.RoomID {
			return fmt.Errorf("target must be an open session in the PR's room")
		}
		if err := sessionlife.Activate(filepath.Join(filepath.Dir(s.Store.Root), "sessions"), to.ID); err != nil {
			return err
		}
		if path != "" {
			r.RepoPath = path
		}
		old := r.Author
		if old == to.ID {
			return nil
		}
		reviewer := r.Reviewer == to.ID

		if reviewer {
			return fmt.Errorf("the reviewer cannot also be the author")
		}
		r.FormerAuthors = append(r.FormerAuthors, r.Author)
		r.Author, r.Parent = to.ID, to.ParentSession

		var successors []Notice
		for i := range r.Notices {
			n := &r.Notices[i]
			if n.Done || n.SupersededBy != "" || n.Target != old {
				continue
			}
			replacement := *n
			replacement.ID = "msg_" + digest(n.ID + "/transfer/" + to.ID)[:16]
			replacement.Target = to.ID
			replacement.PostID, replacement.Error = "", ""
			replacement.Done = false
			n.SupersededBy = replacement.ID
			successors = append(successors, replacement)
		}
		r.Notices = append(r.Notices, successors...)
		notice(r, to.ID, fmt.Sprintf("PR #%d %s role transferred to you. Worktree: %s. Inspect: dyna git pr show %s", r.Number, role, r.Dir, r.URL), fmt.Sprintf("transfer:%s:%s:%s:%d", role, old, to.ID, s.now().UnixNano()))
		return nil
	})
	if err != nil {
		return err
	}
	return s.flush(ctx, key)
}
