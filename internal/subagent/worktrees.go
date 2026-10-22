package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/room"
	"dyna-agent/internal/worktrees"
)

// keptWorktreeReason is the lock reason cleanup and the owner see on a tree
// that a manual handoff asked to keep.
const keptWorktreeReason = "kept for a manual handoff"

// keepWorktrees locks the session's registered worktrees, after its turn has
// stopped and after every check that can refuse the close, so cleanup keeps
// them for a manual handoff. A tree it cannot keep stops the close, and the
// error names the trees already locked, so a partial keep is visible and no
// lock is undone.
func (m *Manager) keepWorktrees(ctx context.Context, info Info) error {
	kept, err := worktrees.KeepSession(ctx, info.ID, keptWorktreeReason)
	if err == nil {
		return nil
	}
	keptNote := ""
	if len(kept) > 0 {
		keptNote = "; already kept: " + strings.Join(kept, ", ")
	}
	return fmt.Errorf("keep worktrees, nothing closed: %w%s", err, keptNote)
}

// startWorktreeCleanup runs cleanupWorktrees after the close commit and the
// session-finish hook. The engine sets Workers, so this stays on that worker
// and stops when the engine context ends. A manager without Workers is not
// the engine. It runs the same body before return, so cleanup cannot keep
// writing after the caller drops its data directory.
func (m *Manager) startWorktreeCleanup(info Info, keep bool) {
	if m.Workers == nil {
		m.cleanupWorktrees(context.Background(), info, keep)
		return
	}
	m.Workers.Go("cleanup "+info.ID, func(workerCtx context.Context) error {
		m.cleanupWorktrees(workerCtx, info, keep)
		return nil
	})
}

// cleanupWorktrees runs after lifecycle locks are released and the
// session-finish hook is queued. The engine runs it on the worker context,
// so shutdown cancels it. A cleanup failure must not undo a successful close
// or allow data loss. Scheduled maintenance retries a tree this pass defers.
// keep locks the session's worktrees again first: a tree registered after the
// close took its locks is not in them, and this pass is what removes trees.
func (m *Manager) cleanupWorktrees(ctx context.Context, info Info, keep bool) {
	if m.checkpoint != nil {
		m.checkpoint("cleanup-start")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if keep {
		if err := m.keepWorktrees(ctx, info); err != nil {
			// A tree that cannot be kept must not be removed by this pass.
			m.reportKeptWorktrees(info, err.Error())
			return
		}
	}
	result, err := maintenance.Run(ctx, maintenance.Options{Session: info.ID, OwnerClosed: func(id string) (bool, error) {
		meta, err := m.store().ReadMeta(id)
		return !meta.Closed.IsZero(), err
	}})
	if kept := cleanupNotice(err, result.Trees); len(kept) > 0 {
		m.reportKeptWorktrees(info, strings.Join(kept, "; "))
	}
}

// cleanupNotice describes what session cleanup kept that someone may need to
// act on. The main checkout is never removed and a clean tree whose owner is
// still open is kept as expected. A cleanup that ran out of time, or that
// engine shutdown canceled, and the trees it deferred, are retried by the
// scheduled maintenance pass. None of these is news.
func cleanupNotice(err error, trees []worktrees.Tree) []string {
	var kept []string
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		kept = append(kept, err.Error())
	}
	for _, a := range trees {
		if a.Action == "removed" || a.Main || a.OwnerOpen || a.Deferred {
			continue
		}
		dirty := "none"
		if len(a.DirtyFiles) > 0 {
			dirty = strings.Join(a.DirtyFiles, ", ")
		}
		ahead := fmt.Sprint(a.Ahead)
		if a.Ahead < 0 {
			ahead = "unknown"
		}
		kept = append(kept, fmt.Sprintf("branch %q, path %q, ahead %s, dirty files: %s (%s)", a.Branch, a.Path, ahead, dirty, a.Reason))
	}
	return kept
}

func (m *Manager) reportKeptWorktrees(info Info, reason string) {
	to, kind, parentID := info.Room, "notice", ""
	if info.Depth != 1 && info.ParentSession != "" {
		if parent, err := m.store().ReadMeta(info.ParentSession); err == nil && parent.Closed.IsZero() && parent.Resumable && parent.HarnessSession != "" && parent.RoomID == info.RoomID {
			to, kind, parentID = Info{Meta: parent}.Address(), "message", parent.ID
		}
	}
	var sessionIDs map[string]string
	if parentID != "" {
		sessionIDs = map[string]string{to: parentID}
	}
	_, err := m.rooms().Post(info.RoomID, room.PostInput{Wake: parentID != "", SessionIDs: sessionIDs, From: info.Address(), Kind: kind, To: []string{to}, Text: fmt.Sprintf("Worktree cleanup kept: %s", strings.ReplaceAll(reason, "\n", " "))})
	if err != nil {
		m.store().appendEvent(info.ID, map[string]any{"t": "worktree_cleanup_report_failed", "error": err.Error()})
		return
	}
}
