package watch

import (
	"context"
	"fmt"
	"path/filepath"

	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/subagent"
)

// transferOptions tune a transfer that a session close starts.
type transferOptions struct {
	// expectAuthor skips the move when another session now holds the author role.
	expectAuthor string
	// inheritUnread re-sends delivered notices that the old author never read.
	inheritUnread bool
}

// Transfer moves one live PR role and preserves the previous author's markers
// or the previous review's provenance. Its caller checks who may transfer. dir
// is the caller's checkout of the PR's repository, or "".
func (s *Service) Transfer(ctx context.Context, key, role string, to subagent.Info, dir string) error {
	_, err := s.transfer(ctx, key, role, to, dir, transferOptions{})
	return err
}

// queuedUnread returns the IDs of posts to session that sit after its room
// cursor, so the session has not read them. A paused room queues posts this way.
func (s *Service) queuedUnread(session string, postIDs map[string]bool) (map[string]bool, error) {
	if s.Notify == nil || s.Notify.Rooms == nil || len(postIDs) == 0 {
		return nil, nil
	}
	meta, err := s.sessionStore().ReadMeta(session)
	if err != nil {
		return nil, err
	}
	posts, _, err := s.Notify.Rooms.ReadAll(meta.RoomID, meta.RoomCursor)
	if err != nil {
		return nil, err
	}
	unread := map[string]bool{}
	for _, p := range posts {
		if postIDs[p.ID] && !meta.ClosedPosts[p.ID] {
			unread[p.ID] = true
		}
	}
	return unread, nil
}

// transfer reports whether the role moved. A skipped stale transfer is not an error.
func (s *Service) transfer(ctx context.Context, key, role string, to subagent.Info, dir string, opts transferOptions) (bool, error) {
	if role != "author" {
		return false, fmt.Errorf("only author transfers are supported; use dyna git pr reviewer --profile or --restart for the managed reviewer")
	}
	// git runs here, never under the PR lock.
	var path string
	readPosts := map[string]string{}
	if r, err := s.Store.Get(key); err == nil {
		path = matchingCheckout(ctx, r, dir)
		if opts.inheritUnread {
			posted := map[string]bool{}
			for _, n := range r.Notices {
				if n.Done && n.PostID != "" && n.SupersededBy == "" && n.Target == opts.expectAuthor {
					posted[n.PostID] = true
				}
			}
			unread, err := s.queuedUnread(opts.expectAuthor, posted)
			if err != nil {
				return false, fmt.Errorf("read queued author notices: %w", err)
			}
			for _, n := range r.Notices {
				if posted[n.PostID] && !unread[n.PostID] {
					readPosts[n.ID] = n.PostID
				}
			}
		}
	}
	if opts.inheritUnread && s.beforeCloseTransfer != nil {
		s.beforeCloseTransfer(key)
	}
	moved := false
	err := s.Store.update(func(st *State) error {
		r := st.Records[key]
		if r == nil || !r.Watching || r.State != "open" {
			return fmt.Errorf("PR is not open and tracked")
		}
		if opts.expectAuthor != "" && r.Author != opts.expectAuthor {
			// An explicit transfer won the race; its author keeps the PR.
			return nil
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
		moved = true
		r.FormerAuthors = append(r.FormerAuthors, r.Author)
		r.Author, r.Parent = to.ID, to.ParentSession

		var successors []Notice
		for i := range r.Notices {
			n := &r.Notices[i]
			if n.SupersededBy != "" || n.Target != old {
				continue
			}
			// Skip only delivery already confirmed read by the scan. A notice
			// delivered since that scan cannot reach the stopped closing session;
			// it must follow the heir even when flush has now marked it Done.
			readPost, read := readPosts[n.ID]
			if n.Done && (!opts.inheritUnread || read && readPost == n.PostID) {
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
		return false, err
	}
	return moved, s.flush(ctx, key)
}
