package goals

import (
	"fmt"
	"time"
)

// Status is one goal's mechanical label with a short reason and its item
// counts. Nothing stores it: every caller computes it from facts.
type Status struct {
	Label   string `json:"label"`
	Reason  string `json:"reason"`
	Done    int    `json:"done"`
	Open    int    `json:"open"`
	Dropped int    `json:"dropped"`
}

// ItemStatus is one item's mechanical label and reason.
type ItemStatus struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// Labels. A merged PR with the goal still open means ready to close, not
// stuck; an open goal with no holders, or an open item with no assignee, is
// unowned. A finished checklist needs a result, so it also reads ready.
const (
	StatusOnTrack      = "on_track"
	StatusErrored      = "errored"
	StatusReadyToClose = "ready_to_close"
	StatusWaiting      = "waiting"
	StatusStuck        = "stuck"
	StatusUnowned      = "unowned"
)

// StaleAfter is how long a holder may sit idle with open work and no journal
// event before the goal reads stuck and earns one nudge (owner-set window).
const StaleAfter = 6 * time.Hour

// Facts carries what a status needs about the sessions working a goal: one
// session's busy state, turn health, PRs, and the goal's newest event.
type Facts struct {
	Busy        bool
	LastFailed  bool
	OpenPR      bool
	MergedPR    bool
	LastEventAt time.Time
}

// freshness is the newest moment the goal's work showed life: its newest
// journal event, its declared wait deadline, or its creation when none
// exists yet.
func freshness(g Goal, f Facts) time.Time {
	at := g.CreatedAt
	if f.LastEventAt.After(at) {
		at = f.LastEventAt
	}
	if g.WaitUntil.After(at) {
		at = g.WaitUntil
	}
	return at
}

// ComputeStatus labels a goal from facts, checked in this order: a running
// turn is on_track (working beats a pending PR, and errored needs nothing
// running), a failed last turn is errored, a closed checklist or a merged PR
// with no open items is ready_to_close, an open goal with no holders is
// unowned, a pending PR is waiting, and an idle holder with open work and no
// event inside the stale window is stuck. Unassigned open items surface in
// the reason and counts without making a held goal unowned.
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
	case len(g.Holders) == 0:
		s.Label, s.Reason = StatusUnowned, "open goal with no holder"
	case f.OpenPR:
		s.Label, s.Reason = StatusWaiting, "PR pending"
	case now.Sub(freshness(g, f)) > StaleAfter:
		s.Label = StatusStuck
		s.Reason = fmt.Sprintf("stale: holder idle with no journal event for %s", now.Sub(freshness(g, f)).Round(time.Minute))
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
	case now.Sub(f.LastEventAt) > StaleAfter:
		return ItemStatus{Label: StatusStuck, Reason: fmt.Sprintf("stale: assignee idle with no event for %s", now.Sub(f.LastEventAt).Round(time.Minute))}
	}
	return ItemStatus{Label: StatusOnTrack, Reason: ""}
}

// Stale reports whether a goal's holder is idle with open work and no event
// inside the stale window: the nudge condition.
func Stale(s Status, f Facts, now time.Time) bool {
	return s.Label == StatusStuck
}
