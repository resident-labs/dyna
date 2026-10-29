package subagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/room"
	"dyna-agent/internal/worktrees"
)

// observePortCapacity reads new and undelivered pool crossings. Tests replace it.
var observePortCapacity = worktrees.ObservePortCapacity

// postPortAlert stores one capacity alert in one room. Tests replace it.
var postPortAlert = func(m *Manager, rec room.Record, alert *worktrees.PortCapacityAlert) error {
	_, err := m.rooms().PostEventInput(room.PostEventInput{
		RoomID: rec.ID,
		Event: room.WorktreePortsEvent{
			Used: alert.Used, Limit: alert.Limit, Threshold: alert.Threshold, Generation: alert.Generation,
		},
		To: []string{rec.Name + ":" + room.RootAgent},
	})
	return err
}

// ReportWorktreePortCapacity observes the pool and posts every new or
// undelivered capacity-band crossing to every room. Delivery is acknowledged
// only after a full sweep of every crossing succeeds, so a transient failure
// is retried on the next observation. Allocation stays best effort: an alert
// failure never rolls back a worktree that Git already created.
func (m *Manager) ReportWorktreePortCapacity() {
	alerts, err := observePortCapacity()
	if err != nil {
		log.Printf("worktree port capacity: %v", err)
		return
	}
	if len(alerts) == 0 || !m.deliverPortCapacityAlerts(alerts) {
		return
	}
	for _, alert := range alerts {
		if err := worktrees.AckPortAlertDelivery(alert.Generation, alert.Threshold); err != nil {
			log.Printf("worktree port capacity delivery: %v", err)
		}
	}
}

// deliverPortCapacityAlerts posts each band crossing, oldest first, to the
// root seat of every room: the pool is host-wide, so every room's root can
// act on it. It reports whether every room received every crossing.
func (m *Manager) deliverPortCapacityAlerts(alerts []*worktrees.PortCapacityAlert) bool {
	rooms, err := m.rooms().List(false)
	if err != nil {
		log.Printf("worktree port capacity rooms: %v", err)
		return false
	}
	delivered := true
	for _, rec := range rooms {
		for _, alert := range alerts {
			if err := postPortAlert(m, rec, alert); err != nil {
				log.Printf("worktree port capacity alert for %s: %v", rec.Name, err)
				delivered = false
			}
		}
	}
	return delivered
}

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
			m.reportKeptWorktree(info, keptTree{Reason: err.Error()})
			return
		}
	}
	result, err := maintenance.Run(ctx, maintenance.Options{Session: info.ID, OwnerClosed: func(id string) (bool, error) {
		meta, err := m.store().ReadMeta(id)
		return !meta.Closed.IsZero(), err
	}})
	for _, k := range cleanupNotice(err, result.Trees) {
		m.reportKeptWorktree(info, k)
	}
}

// keptTree is one fact cleanup kept that someone may need to act on.
type keptTree struct {
	Owner, Path, Branch, Reason string
	Ahead                       int
	Dirty                       []string
}

// cleanupNotice lists what session cleanup kept that someone may need to
// act on. The main checkout is never removed and a clean tree whose owner is
// still open is kept as expected. A cleanup that ran out of time, or that
// engine shutdown canceled, and the trees it deferred, are retried by the
// scheduled maintenance pass. None of these is news.
func cleanupNotice(err error, trees []worktrees.Tree) []keptTree {
	var kept []keptTree
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		kept = append(kept, keptTree{Reason: err.Error()})
	}
	for _, a := range trees {
		if a.Action == "removed" || a.Main || a.OwnerOpen || a.Deferred {
			continue
		}
		dirty := a.DirtyFiles
		ahead := a.Ahead
		if ahead < 0 {
			ahead = 0
		}
		kept = append(kept, keptTree{Owner: a.Owner, Path: a.Path, Branch: a.Branch, Ahead: ahead, Dirty: dirty, Reason: a.Reason})
	}
	return kept
}

// reportKeptWorktree tells the session that owns the tree, and only it, that
// cleanup kept the tree. The post is quiet, comes from the engine and is
// stored once per tree and reason: PostEvent derives the post ID from them,
// so a later pass that finds the same tree kept again posts nothing. A tree
// with no known owner, and a failure to keep, go to the session being closed.
func (m *Manager) reportKeptWorktree(info Info, k keptTree) {
	owner := info.Meta
	if k.Owner != "" && k.Owner != info.ID {
		if meta, err := m.store().ReadMeta(k.Owner); err == nil && meta.RoomID == info.RoomID {
			owner = meta
		}
	}
	_, err := m.rooms().PostEventInput(room.PostEventInput{
		RoomID: info.RoomID,
		Event: room.WorktreeEvent{
			Owner:  owner.ID,
			Path:   k.Path,
			Branch: k.Branch,
			Ahead:  k.Ahead,
			Dirty:  k.Dirty,
			Reason: worktreeReason(k),
			Detail: strings.ReplaceAll(k.Reason, "\n", " "),
		},
		To:         []string{owner.Address()},
		SessionIDs: map[string]string{owner.Address(): owner.ID},
	})
	if err != nil {
		m.store().appendJournal(info.ID, WorktreeReportFailedJournal{Error: err.Error()})
	}
}

// worktreeReason picks the reason class of a kept tree. The class keys the
// dedupe and the card wording; the detail keeps the full sentence.
func worktreeReason(k keptTree) room.WorktreeReason {
	switch {
	case k.Path == "":
		return room.WorktreeKeepFailed
	case strings.HasPrefix(k.Reason, worktrees.ReasonNotMerged):
		return room.WorktreeNotMerged
	case strings.HasPrefix(k.Reason, worktrees.ReasonDirty):
		return room.WorktreeDirty
	case strings.Contains(k.Reason, keptWorktreeReason):
		return room.WorktreeManualHandoff
	default:
		return room.WorktreeOther
	}
}
