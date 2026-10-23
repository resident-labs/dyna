package watch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"dyna-agent/internal/pr/state"
)

// ErrPendingVerdict is a successful save awaiting remote head confirmation.
type ErrPendingVerdict struct{}

func (*ErrPendingVerdict) Error() string {
	return "verdict saved; applies after GitHub refresh"
}

// VerdictNotice identifies a saved or obsolete review that needs no failure report.
func VerdictNotice(err error) (string, bool) {
	var pending *ErrPendingVerdict
	var stale *ErrStaleVerdict
	if errors.As(err, &pending) {
		return pending.Error(), true
	}
	if errors.As(err, &stale) {
		return stale.Error(), true
	}
	return "", false
}

func validateVerdictReviewer(r Record, actor, head string, reviewID int64, named bool) error {
	if !r.Watching || r.State != "open" {
		return fmt.Errorf("PR is not open and watched")
	}
	if actor == r.Author {
		return fmt.Errorf("the author cannot review its own PR")
	}
	if actor == "" || (r.Reviewer != actor && !named) {
		return fmt.Errorf("this session is not the assigned reviewer")
	}
	if named && head == "" {
		return fmt.Errorf("a named reviewer must supply --head")
	}
	if head == "" {
		return fmt.Errorf("a local review must supply --head from its assignment")
	}
	return nil
}

// pendingVerdictSlot finds one review or the tail where a new reviewer belongs.
func pendingVerdictSlot(r *Record, actor, head string) **state.PendingVerdict {
	slot := &r.PendingVerdict
	for *slot != nil {
		if (*slot).Actor == actor && (*slot).Head == head {
			break
		}
		slot = &(*slot).Next
	}
	return slot
}

// Author replies can change while a saved submission awaits replay.
func submissionFindings(in []Finding) []Finding {
	out := make([]Finding, len(in))
	copy(out, in)
	for i := range out {
		out[i].Replies = nil
	}
	return out
}

func samePendingVerdict(a, b *state.PendingVerdict) bool {
	left, right := *a, *b
	left.Next, right.Next = nil, nil
	left.Findings, right.Findings = submissionFindings(left.Findings), submissionFindings(right.Findings)
	return reflect.DeepEqual(left, right)
}

func removePendingVerdict(r *Record, pending *state.PendingVerdict) bool {
	slot := pendingVerdictSlot(r, pending.Actor, pending.Head)
	if *slot == nil || !samePendingVerdict(*slot, pending) {
		return false
	}
	*slot = (*slot).Next
	return true
}

// recordedVerdict is shared by immediate and paused per-reviewer retries.
func recordedVerdict(r Record, p *state.PendingVerdict) (bool, error) {
	if r.RoundHead != p.Head {
		return false, nil
	}
	for _, prior := range r.ReviewRounds {
		if prior.Round != r.Round || prior.Head != p.Head || prior.Reviewer != p.Actor {
			continue
		}
		if prior.Verdict == p.Verdict && prior.Summary == p.Summary && prior.ReviewID == p.ReviewID && reflect.DeepEqual(submissionFindings(prior.Findings), submissionFindings(p.Findings)) {
			return true, nil
		}
		return false, fmt.Errorf("a verdict already exists for this head")
	}
	return false, nil
}

func (s *Service) savePendingVerdict(key, actor, head, verdict, summary string, reviewID int64, findings []Finding) error {
	pending := &state.PendingVerdict{Actor: actor, Head: head, Verdict: verdict, Summary: summary, ReviewID: reviewID, Findings: cloneFindings(findings)}
	saved := false
	err := s.Store.update(func(st *State) error {
		r := st.Records[key]
		if r == nil {
			return fmt.Errorf("PR is not tracked")
		}
		named := s.namedReviewer(*st, *r, actor)
		if err := validateVerdictReviewer(*r, actor, head, reviewID, named); err != nil {
			return err
		}
		if head == "" || head != r.Head || (!named && head != r.RoundHead) {
			return fmt.Errorf("pending verdict must supply the assigned head")
		}
		if EffectivePolicy(*st, *r).Review == "off" && !named {
			return fmt.Errorf("review is off for this change")
		}
		if done, err := recordedVerdict(*r, pending); done || err != nil {
			return err
		}
		slot := pendingVerdictSlot(r, actor, head)
		if *slot != nil && !samePendingVerdict(*slot, pending) {
			return fmt.Errorf("a pending verdict already exists for this head")
		}
		if *slot == nil {
			*slot = pending
		}
		saved = true
		// A full refresh validates published review IDs and comments too.
		r.Polling.FullChecked = time.Time{}
		r.Polling.Next = time.Time{}
		return nil
	})
	if err != nil || !saved {
		return err
	}
	return &ErrPendingVerdict{}
}

func (s *Service) replayPendingVerdicts(ctx context.Context, heads map[string]headProbe) error {
	st, err := s.readTickState(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, key := range tickKeys(st) {
		if err := ctx.Err(); err != nil {
			return errors.Join(first, err)
		}
		r := st.Records[key]
		probe, ok := heads[key]
		if r.PendingVerdict == nil || !ok || probe.Err != nil {
			continue
		}
		recordCtx, cancel := context.WithTimeout(ctx, s.recordBudget())
		var snap *Snapshot
		for pending := r.PendingVerdict; pending != nil; pending = pending.Next {
			if probe.Pull.Head.SHA != pending.Head || probe.Pull.State != "open" {
				first = errors.Join(first, s.rejectPendingVerdict(key, pending, "GitHub head changed or the PR closed"))
				continue
			}
			if snap == nil {
				value, e := s.github(r.RoomID).Snapshot(recordCtx, refRecord(*r))
				if e != nil {
					first = errors.Join(first, e)
					break
				}
				snap = &value
			}
			e := s.verdictWithSnapshot(recordCtx, key, pending.Actor, pending.Head, pending.Verdict, pending.Summary, pending.ReviewID, pending.Findings, snap)
			if e != nil && recordCtx.Err() == nil {
				// Delivery can fail after commit; reject only a still-saved matching review.
				fresh, readErr := s.Store.Get(key)
				if readErr == nil {
					slot := pendingVerdictSlot(&fresh, pending.Actor, pending.Head)
					if *slot != nil && samePendingVerdict(*slot, pending) {
						e = s.rejectPendingVerdict(key, pending, e.Error())
					}
				}
			}
			first = errors.Join(first, e)
		}
		cancel()
	}
	return first
}

func (s *Service) rejectPendingVerdict(key string, pending *state.PendingVerdict, reason string) error {
	return s.Store.update(func(st *State) error {
		r := st.Records[key]
		if r == nil || !removePendingVerdict(r, pending) {
			return nil
		}
		notice(r, pending.Actor, fmt.Sprintf("Saved verdict for %s at %s was not applied: %s.", changeLabel(*r), pending.Head, clip(reason, 300)), "pending-verdict-rejected:"+pending.Head+":"+pending.Actor, nil)
		return nil
	})
}
