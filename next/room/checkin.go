package room

import (
	"cmp"
	"slices"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const (
	// CheckinInterval is the least time between two check-ins to one session. Each one costs a model turn.
	CheckinInterval = 30 * time.Minute
	// CheckinIdle is how long a goal holder may sit still before its parent hears of it.
	CheckinIdle = 10 * time.Minute
	// CheckinEscalate is how long one ancestor has with a stall before the next one up hears of it.
	CheckinEscalate = time.Hour
	// CheckinStuck is how long a turn, a background job or a box start may last before its goal holder counts as blocked.
	CheckinStuck = 2 * time.Hour
	// CheckinRows caps the rows one check-in shows.
	CheckinRows = 12
)

// CheckinSession holds what the engine knows about one open session of a room when it plans check-ins.
type CheckinSession struct {
	ID      contract.SessionID
	Parent  contract.SessionID
	Name    string
	Closing bool

	Box        contract.BoxState
	BoxChanged time.Time // when the box reached its state
	Turn       bool      // a turn is running
	Last       time.Time // when the running turn started, the last turn ended, or the session was created
	Failed     bool      // the last turn failed
	Retrying   bool      // a model retry is scheduled
	Jobs       int       // background shell jobs still running
	Pending    bool      // a waking input or a running operation will move it
	Asking     bool      // it has an open question for the owner; a report says so, and it excuses nothing
	Blocked    bool      // one of those questions left it nothing safe to do, so only the answer will move it
	Waiting    time.Time // when its oldest undecided operation was asked; zero for none

	// A session with no goal reports nothing. Goal is its oldest active goal.
	Goal     contract.GoalID
	Text     string
	Open     int
	Progress time.Time // the newest change to any goal it holds
	Silent   bool      // its last turn ended with an open goal and no reply

	Told   int       // how many ancestors have heard of this stall
	ToldAt time.Time // when the last of them did

	Reported time.Time // when it last received a check-in or failure notice
	Unread   bool      // that report has not reached the model

	Failure     string // FailureBox, FailureTurn, or empty
	FailureTold string // the failure its ancestors already know of
	Reason      string // the box's stored reason
}

// A session's failure, the same one until it recovers, so a run of failed retries is told once.
const (
	FailureBox  = "box"
	FailureTurn = "turn"
)

// canRead reports whether a report sent here would reach a model.
func (s CheckinSession) canRead(now time.Time) bool {
	broken := s.Box == contract.BoxFailed || s.Box == contract.BoxLost
	return !s.Closing && !broken && (!s.Unread || now.Sub(s.Reported) < CheckinEscalate)
}

// CheckinPlan is one report to send, with the new told count of each child in it.
type CheckinPlan struct {
	To       contract.SessionID
	Children []contract.CheckinChild
	Told     map[contract.SessionID]int
}

// stillness says how a session that is not moving sits: a state, why, and since when. moving is true while something will
// still move it. Past CheckinStuck a turn, a job or a box start no longer counts as moving.
func (s CheckinSession) stillness(now time.Time) (state contract.CheckinState, reason string, since time.Time, moving bool) {
	stuck := func(from time.Time) bool { return now.Sub(from) >= CheckinStuck }
	switch s.Box {
	case contract.BoxFailed, contract.BoxLost:
		return contract.CheckinBlocked, "its box failed", s.BoxChanged, false
	case contract.BoxPending, contract.BoxCreating, contract.BoxStarting:
		return contract.CheckinBlocked, "its box has not started", s.BoxChanged, !stuck(s.BoxChanged)
	}
	switch {
	case s.Turn:
		return contract.CheckinBlocked, "its turn has run a long time", s.Last, !stuck(s.Last)
	case s.Jobs > 0:
		return contract.CheckinBlocked, "its background job has run a long time", s.Last, !stuck(s.Last)
	case s.Pending, s.Retrying, s.Blocked:
		return "", "", time.Time{}, true
	case !s.Waiting.IsZero():
		return contract.CheckinBlocked, "it waits on an operation", s.Waiting, false
	case s.Failed:
		return contract.CheckinBlocked, "its last turn failed", s.Last, false
	case s.Box == contract.BoxStopped:
		return contract.CheckinStopped, "", s.Last, false
	}
	return contract.CheckinIdle, "", s.Last, false
}

// PlanCheckins picks who hears of which stalled session now. A stall is a goal holder that nothing moves, with nothing
// moving below it either: a lead that waits on working children is not stalled. Each stall goes to the parent once, then up
// one ancestor per CheckinEscalate until root has it. Time alone drives that, so a parent that never ends its turn cannot
// hold a stall back. The caller resets Told when the goal moves on.
func PlanCheckins(sessions []CheckinSession, now time.Time) []CheckinPlan {
	byID := make(map[contract.SessionID]*CheckinSession, len(sessions))
	for i := range sessions {
		byID[sessions[i].ID] = &sessions[i]
	}
	busyBelow := make(map[contract.SessionID]bool)
	for _, s := range sessions {
		if _, _, _, moving := s.stillness(now); moving {
			for p := byID[s.Parent]; p != nil && !busyBelow[p.ID]; p = byID[p.Parent] {
				busyBelow[p.ID] = true
			}
		}
	}
	plans := make(map[contract.SessionID]*CheckinPlan)
	for _, s := range sessions {
		if s.Goal == "" || s.Closing || busyBelow[s.ID] || s.Failure != "" && s.Failure != s.FailureTold {
			continue // A failure goes out as its own notice, first.
		}
		state, reason, since, moving := s.stillness(now)
		if moving {
			continue
		}
		if s.Progress.After(since) {
			since = s.Progress
		}
		// A silent turn counts until someone records progress after it.
		if silent := s.Silent && !s.Progress.After(s.Last); !silent && now.Sub(since) < CheckinIdle {
			continue
		}
		if s.Told > 0 && now.Sub(s.ToldAt) < CheckinEscalate {
			continue
		}
		var above []*CheckinSession
		for p := byID[s.Parent]; p != nil; p = byID[p.Parent] {
			above = append(above, p)
		}
		for i := s.Told; i < len(above); i++ {
			to := above[i]
			if !to.canRead(now) {
				continue // The stall goes past it.
			}
			plan := plans[to.ID]
			if plan == nil {
				if to.Unread || now.Sub(to.Reported) < CheckinInterval {
					break
				}
				plan = &CheckinPlan{To: to.ID, Told: make(map[contract.SessionID]int)}
				plans[to.ID] = plan
			}
			child := contract.CheckinChild{Session: s.ID, Name: s.Name, State: state, Reason: reason, Idle: contract.DurationMS(now.Sub(since).Milliseconds()), Silent: s.Silent, Asking: s.Asking, Goal: s.Goal, Text: s.Text, Open: s.Open}
			if i > 0 {
				child.Parent = byID[s.Parent].Name
			}
			plan.Children = append(plan.Children, child)
			plan.Told[s.ID] = i + 1
			break
		}
	}
	result := make([]CheckinPlan, 0, len(plans))
	for _, plan := range plans {
		slices.SortFunc(plan.Children, func(a, b contract.CheckinChild) int {
			return cmp.Or(cmp.Compare(b.Idle, a.Idle), cmp.Compare(a.Session, b.Session))
		})
		result = append(result, *plan)
	}
	slices.SortFunc(result, func(a, b CheckinPlan) int { return cmp.Compare(a.To, b.To) })
	return result
}

// FailurePlan is one failure notice to send, with the told count of each session in it.
type FailurePlan struct {
	To       contract.SessionID
	Failures []contract.SessionFailure
	Told     map[contract.SessionID]int
}

// PlanFailures picks who hears of each new failure. The parent hears first, or the nearest ancestor that can read, with root
// as the last resort. There is no wait and no rate limit, because a failed session stops until someone acts; each failure
// goes out once. settled holds every session whose recorded failure changes, sent or not: a recovered session clears its
// record, and a turn failure left behind by a box failure is old news.
func PlanFailures(sessions []CheckinSession, now time.Time) (plans []FailurePlan, settled map[contract.SessionID]string) {
	byID := make(map[contract.SessionID]*CheckinSession, len(sessions))
	for i := range sessions {
		byID[sessions[i].ID] = &sessions[i]
	}
	settled = make(map[contract.SessionID]string)
	index := make(map[contract.SessionID]int)
	for _, s := range sessions {
		if s.Closing || s.Failure == s.FailureTold {
			continue
		}
		settled[s.ID] = s.Failure
		if s.Failure == "" || s.Failure == FailureTurn && s.FailureTold == FailureBox {
			continue
		}
		var above []*CheckinSession
		for p := byID[s.Parent]; p != nil; p = byID[p.Parent] {
			above = append(above, p)
		}
		if len(above) == 0 {
			continue
		}
		at := slices.IndexFunc(above, func(p *CheckinSession) bool { return p.canRead(now) })
		if at < 0 {
			at = len(above) - 1
		}
		to := above[at]
		i, ok := index[to.ID]
		if !ok {
			i = len(plans)
			index[to.ID] = i
			plans = append(plans, FailurePlan{To: to.ID, Told: make(map[contract.SessionID]int)})
		}
		failure := contract.SessionFailure{Session: s.ID, Name: s.Name, Reason: s.Reason}
		if s.Failure == FailureBox {
			failure.Box = s.Box
		}
		if at > 0 {
			failure.Parent = byID[s.Parent].Name
		}
		plans[i].Failures = append(plans[i].Failures, failure)
		plans[i].Told[s.ID] = max(s.Told, at+1)
	}
	return plans, settled
}
