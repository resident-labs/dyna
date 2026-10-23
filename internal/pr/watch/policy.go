package watch

import (
	"fmt"
	"slices"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/changepolicy"
	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/runstore"
)

type ChangePolicy = changepolicy.Policy
type PolicyOverride = changepolicy.Override

// RoomPolicy returns the stored policy or the defaults for a new room.
func RoomPolicy(st State, roomID string) ChangePolicy {
	p, ok := st.Policies[roomID]
	if !ok {
		p = changepolicy.Default()
	}
	if p.CITimeBudget == "" {
		p.CITimeBudget = "warn"
	}
	return p
}
func EffectivePolicy(st State, r Record) ChangePolicy {
	return changepolicy.Apply(RoomPolicy(st, r.RoomID), r.PolicyOverride)
}

func (s *Service) RoomChangePolicy(roomID string) (ChangePolicy, error) {
	st, err := s.Store.Read()
	if err != nil {
		return ChangePolicy{}, err
	}
	return RoomPolicy(st, roomID), nil
}
func (s *Service) SetRoomChangePolicy(roomID string, actor authority.Actor, p ChangePolicy) error {
	if err := authority.Check("SetRoomChangePolicy", actor, nil); err != nil {
		return err
	}
	if roomID == "" {
		return fmt.Errorf("room is required")
	}
	if err := changepolicy.Validate(p); err != nil {
		return err
	}
	for _, id := range p.Reviewers {
		if !runstore.IsSessionDirID(id) {
			return fmt.Errorf("reviewer %q must be an exact session ID", id)
		}
		info, err := s.sessionStore().ReadMeta(id)
		if err != nil || info.ID != id || info.RoomID != roomID || !info.Closed.IsZero() {
			return fmt.Errorf("reviewer %q must be an open session in this room", id)
		}
	}
	p.Reviewers = slices.Clone(p.Reviewers)
	return s.Store.update(func(st *State) error {
		if st.Policies == nil {
			st.Policies = map[string]ChangePolicy{}
		}
		st.Policies[roomID] = p
		cfg := st.Reviewers[roomID]
		cfg.Disabled = p.Review == "off"
		st.Reviewers[roomID] = cfg
		clearDisabledAssignment(st, roomID)
		return nil
	})
}

// SetChangePolicyOverride sets one change's policy override. Its caller
// checks who may set it against author, the change's author it saw.
func (s *Service) SetChangePolicyOverride(key, author string, o PolicyOverride) error {
	o.Reason = Sanitize(strings.TrimSpace(o.Reason))
	if err := changepolicy.ValidateOverride(o); err != nil {
		return err
	}
	if o.Empty() {
		o = PolicyOverride{}
	}
	return s.Store.update(func(st *State) error {
		r := recordByID(st, key)
		if r == nil {
			return fmt.Errorf("change is not tracked")
		}
		if r.State != "open" {
			return fmt.Errorf("policy overrides require an open change")
		}
		if err := sameAuthor(r, author); err != nil {
			return err
		}
		r.PolicyOverride = o
		clearDisabledAssignment(st, r.RoomID)
		return nil
	})
}

// headVerified is the shared gate for actions that depend on the PR head.
// A failed refresh blocks actions until a complete refresh succeeds.
func headVerified(r Record) bool {
	return prfacts.HeadVerified(r)
}

// policyReviewReady permits review in parallel with CI for a verified head.
func policyReviewReady(st State, r Record) bool {
	return EffectivePolicy(st, r).Review != "off" && headVerified(r)
}

// A policy change retires an assignment that no longer needs review. The
// session can finish its current turn, but it cannot sign the skipped change.
func clearDisabledAssignment(st *State, roomID string) {
	for _, r := range st.Records {
		if r.RoomID != roomID || EffectivePolicy(*st, *r).Review != "off" {
			continue
		}
		retireReviewNotices(r, "review-disabled-by-policy")
		if r.Verdict == "" {
			rememberReviewAssignment(r)
			r.Reviewer = ""
			r.RoundHead = ""
		}
	}
}

// namedReviewer uses configured exact IDs, never session labels or service roles.
func (s *Service) namedReviewer(st State, r Record, actor string) bool {
	if actor == "" || (!slices.Contains(RoomPolicy(st, r.RoomID).Reviewers, actor) && !slices.Contains(r.RequiredReviewers, actor)) {
		return false
	}
	info, err := s.sessionStore().ReadMeta(actor)
	return err == nil && info.ID == actor && info.RoomID == r.RoomID && info.Closed.IsZero()
}
