package goals

import (
	"fmt"
	"time"
)

// ItemStatus is one item's mechanical label and reason.
type ItemStatus struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// Labels. A merged PR with the goal still open means ready to close, not
// stuck; an open goal with no holders, or an open item with no assignee, is
// unowned. A goal an old build left with the owner needs root. A finished
// checklist needs a result, so it also reads ready.
const (
	StatusOnTrack      = "on_track"
	StatusErrored      = "errored"
	StatusReadyToClose = "ready_to_close"
	StatusWaiting      = "waiting"
	StatusStuck        = "stuck"
	StatusUnowned      = "unowned"
	StatusNeedsRoot    = "needs_root"
)

// Facts carries what a status needs about the sessions working a goal: one
// session's busy state, turn health, and PRs.
type Facts struct {
	Busy       bool
	LastFailed bool
	OpenPR     bool
	MergedPR   bool
}

// ComputeStatus labels a goal from facts, checked in this order: a running
// turn is on_track (working beats a pending PR, and errored needs nothing
// running), a failed last turn is errored, a closed checklist or a merged PR
// with no open items is ready_to_close, a goal an old build left with the
// owner needs root, an open goal with no holders is unowned, a pending PR is
// waiting, and other held goals are on_track. Unassigned open items surface
// in the reason and counts without making a held goal unowned.
func ComputeStatus(g Goal, items []Item, f Facts, now time.Time) Status {
	var s Status
	for _, it := range items {
		switch it.State {
		case ItemDone:
			s.Done++
		case ItemDropped:
			s.Dropped++
		default:
			s.Open++
		}
	}
	unassigned := 0
	for _, it := range items {
		if it.State == ItemOpen && it.Assignee == "" {
			unassigned++
		}
	}
	if !g.Open() {
		// Keep the counted items; only the label and reason change.
		s.Label, s.Reason = g.State, "goal "+g.State
		return s
	}
	s.Label = StatusOnTrack
	switch {
	case f.Busy:
		s.Reason = "turn running"
	case f.LastFailed:
		s.Label, s.Reason = StatusErrored, "last turn failed"
	case s.Open == 0 && (len(items) > 0 || f.MergedPR):
		s.Label = StatusReadyToClose
		s.Reason = "all items closed; finish the goal"
		if f.MergedPR {
			s.Reason = "merged PR with no open items; finish the goal"
		}
	case g.NeedsRoot():
		s.Label, s.Reason = StatusNeedsRoot, "root asks the owner, then drops or finishes the goal"
	case len(g.Holders) == 0:
		s.Label, s.Reason = StatusUnowned, "open goal with no holder"
	case f.OpenPR:
		s.Label, s.Reason = StatusWaiting, "PR pending"
	case f.MergedPR:
		s.Reason = fmt.Sprintf("merged PR with %d items still open", s.Open)
	}
	if unassigned > 0 {
		if s.Reason != "" {
			s.Reason += "; "
		}
		s.Reason += fmt.Sprintf("%d unassigned open item(s)", unassigned)
	}
	return s
}

// ComputeItemStatus labels one item from its assignee's facts. Item labels
// are coarse: a busy assignee may be running a turn for another item, so the
// reason says so. Closed items carry their own state as the label.
func ComputeItemStatus(item Item, f Facts, now time.Time) ItemStatus {
	switch item.State {
	case ItemDone:
		return ItemStatus{Label: ItemDone, Reason: "done"}
	case ItemDropped:
		return ItemStatus{Label: ItemDropped, Reason: "dropped"}
	}
	switch {
	case item.Assignee == "":
		return ItemStatus{Label: StatusUnowned, Reason: "no assignee"}
	case f.Busy:
		return ItemStatus{Label: StatusOnTrack, Reason: "assignee busy (may be another item)"}
	case f.LastFailed:
		return ItemStatus{Label: StatusErrored, Reason: "assignee's last turn failed"}
	case f.OpenPR:
		return ItemStatus{Label: StatusWaiting, Reason: "assignee has a PR pending"}
	}
	return ItemStatus{Label: StatusOnTrack, Reason: ""}
}
