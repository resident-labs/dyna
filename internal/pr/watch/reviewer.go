package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

type ReviewerSession struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Status  string `json:"status"`
}
type ReviewerHelper struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Profile string `json:"profile"`
	Status  string `json:"status"`
}
type ReviewCurrent struct {
	ChangeID  string    `json:"changeId,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	PR        int       `json:"pr"`
	Title     string    `json:"title"`
	Head      string    `json:"head"`
	StartedAt time.Time `json:"startedAt"`
}
type ReviewQueued struct {
	GoalID    string `json:"goalId,omitempty"`
	Expedited bool   `json:"expedited,omitempty"`
	ChangeID  string `json:"changeId,omitempty"`
	Kind      string `json:"kind,omitempty"`
	PR        int    `json:"pr"`
	Title     string `json:"title"`
	Head      string `json:"head"`
}
type ReviewerSlotStatus struct {
	Session   *ReviewerSession `json:"session"`
	Profile   string           `json:"profile"`
	Current   *ReviewCurrent   `json:"current"`
	Status    string           `json:"status"`
	LastError string           `json:"lastError"`
}
type ReviewerStatus struct {
	ExpeditedGoalID string               `json:"expeditedGoalId,omitempty"`
	Reviewers       []ReviewerSlotStatus `json:"reviewers"`
	Helpers         []ReviewerHelper     `json:"helpers"`

	Enabled       bool                 `json:"enabled"`
	Profile       string               `json:"profile"`
	ProfileSource string               `json:"profileSource"`
	ProfileReason string               `json:"profileReason"`
	Queue         []ReviewQueued       `json:"queue"`
	LastError     string               `json:"lastError"`
	Recent        []state.ReviewResult `json:"recent"`
}

func (s *Service) profiles() ([]profile.Profile, error) {
	if s.ReviewerProfiles != nil {
		return s.ReviewerProfiles()
	}
	if s.Notify == nil {
		return nil, fmt.Errorf("reviewer profile store is unavailable")
	}
	ps, err := s.Notify.Sessions.ProfileStore()
	if err != nil {
		return nil, err
	}
	return ps.Profiles, nil
}

func (s *Service) chooseProfile(cfg Reviewer) (string, string, error) {
	profiles, err := s.profiles()
	if err != nil {
		return "", "", err
	}
	recent, err := s.recentProfileFailures()
	if err != nil {
		return "", "", err
	}
	return rolesession.ChooseProfile(profiles, cfg.Profile, rolesession.MergeFailures(cfg.FailedProfiles, recent), s.now(), subagent.RoleReviewer)
}

func (s *Service) SetReviewEnabled(roomID string, enabled bool) error {
	return s.Store.update(func(st *State) error {
		cfg := st.Reviewers[roomID]
		cfg.Disabled = !enabled
		policy := RoomPolicy(*st, roomID)
		if enabled {
			policy.Review = "on"
		} else {
			policy.Review = "off"
		}
		if st.Policies == nil {
			st.Policies = map[string]ChangePolicy{}
		}
		st.Policies[roomID] = policy
		st.Reviewers[roomID] = cfg
		clearDisabledAssignment(st, roomID)
		return nil
	})
}

// Reviewer actions have their own lock. Never hold the PR state lock while
// starting or closing sessions (these take hierarchy locks).

func (s *Service) reviewerLock(roomID string) (*os.File, error) {
	return rolesession.Lock(s.Store.Root, "reviewer", roomID)
}

func unlockReviewer(f *os.File) { rolesession.Unlock(f) }

func retireReviewNotices(r *Record, reason string) {
	retireSessionReviewNotices(r, r.Reviewer, reason)
}

func retireSessionReviewNotices(r *Record, session, reason string) {
	if session == "" {
		return
	}
	for i := range r.Notices {
		n := &r.Notices[i]
		if !n.Done && n.SupersededBy == "" && n.Target == session && reviewNotice(*n) {
			n.SupersededBy = reason
		}
	}
}

func (s *Service) recentProfileFailures() (map[string]time.Time, error) {
	if s.Notify == nil {
		return nil, nil
	}
	return rolesession.RecentProfileFailures(s.Notify.Sessions.Store, s.now())
}

func reviewerChangeName(r *Record) string {
	if r == nil {
		return ""
	}
	if changeKind(*r) == "github" {
		return fmt.Sprintf("reviewer-%d", r.Number)
	}
	id := strings.TrimPrefix(changeID(*r), "chg_")
	if len(id) > 12 {
		id = id[:12]
	}
	return "reviewer-" + id
}

func (s *Service) reviewHeadCurrent(ctx context.Context, r Record) (bool, error) {
	if changeKind(r) != "github" {
		return true, nil
	}
	pull, err := s.github(r.RoomID).Pull(ctx, refRecord(r))
	if err != nil {
		return false, err
	}
	if pull.State != "closed" {
		return !pull.Draft && prfacts.HeadCurrent(r, pull.Head.SHA), nil
	}
	return false, s.Store.update(func(st *State) error {
		if live := st.Records[r.Key]; live != nil {
			live.Polling.Next = time.Time{}
		}
		return nil
	})
}

func (s *Service) closeReviewer(ctx context.Context, id string) error {
	if s.CloseReviewer != nil {
		return s.CloseReviewer(ctx, id)
	}
	if s.Notify == nil {
		return nil
	}
	_, err := s.Notify.Sessions.CloseWithOptions(ctx, id, subagent.CloseOptions{Force: true})
	return err
}

func rememberReviewAssignment(r *Record) {
	if r.Reviewer == "" || r.RoundHead == "" {
		return
	}
	r.LastReviewer, r.LastReviewHead = r.Reviewer, r.RoundHead
	if r.ReviewAssignments == nil {
		r.ReviewAssignments = map[string][]string{}
	}
	for _, actor := range r.ReviewAssignments[r.RoundHead] {
		if actor == r.Reviewer {
			return
		}
	}
	r.ReviewAssignments[r.RoundHead] = append(r.ReviewAssignments[r.RoundHead], r.Reviewer)
}

// closedReviewerWindow is how long ReviewFacts still reads the reviewers of a
// closed PR, so the pool can release them.
const closedReviewerWindow = time.Hour

func (s *Service) sessionStore() *subagent.Store {
	if s.Notify != nil {
		return s.Notify.Sessions.Store
	}
	return subagent.DefaultStore()
}

// reviewerInfo uses the session store's live turn lock, not the PR round state.
func (s *Service) reviewerInfo(id string) (subagent.Info, subagent.Turn, error) {
	if s.ReviewerInfo != nil {
		return s.ReviewerInfo(id)
	}
	store := s.sessionStore()
	info, err := store.Inspect(id)
	if err != nil {
		return info, subagent.Turn{}, err
	}
	turn, err := store.ReadTurn(info.ID, info.Turns)
	if os.IsNotExist(err) {
		err = nil
	}
	return info, turn, err
}

type reviewerTurn struct {
	Info subagent.Info
	Turn subagent.Turn
}

// ReviewFacts is one observation shared by dispatch, queue and client status.
// None of these facts are persisted as pool occupancy or review status.
type ReviewFacts struct {
	ExpeditedGoals map[string]string
	Streaks        map[string]int
	State          State
	Need           []*Record
	Busy           map[string]reviewerTurn
	Sessions       map[string]reviewerTurn
	Paused         map[string]bool
}

func needsReview(st State, r Record) bool {
	return prfacts.NeedsReview(r) && EffectivePolicy(st, r).Review != "off"
}

// RoomPaused reads the authoritative room pause used by background PR workers.
func (s *Service) RoomPaused(roomID string) (bool, error) {
	if s.Notify == nil || s.Notify.Rooms == nil {
		return false, nil
	}
	paused, err := s.Notify.Rooms.Paused(roomID)
	// Keep saved history readable without starting work for a deleted room.
	if errors.Is(err, room.ErrNotFound) {
		return true, nil
	}
	return paused, err
}

// roomPausedCached shares one room observation across a background polling pass.
// Explicit actions without a cache always read the current room state.
func (s *Service) roomPausedCached(roomID string, cache map[string]bool) (bool, error) {
	if cache == nil {
		return s.RoomPaused(roomID)
	}
	if paused, ok := cache[roomID]; ok {
		return paused, nil
	}
	paused, err := s.RoomPaused(roomID)
	if err == nil {
		cache[roomID] = paused
	}
	return paused, err
}

func (s *Service) ReviewFacts(st State) (ReviewFacts, error) {
	facts := ReviewFacts{State: st, Busy: map[string]reviewerTurn{}, Sessions: map[string]reviewerTurn{}, Paused: map[string]bool{}}
	active, _, err := s.expeditedGoals()
	if err != nil {
		return facts, err
	}
	facts.ExpeditedGoals, facts.Streaks = active, map[string]int{}
	ids := map[string]bool{}
	if s.Notify != nil {
		infos, err := s.Notify.Sessions.Store.ListSnapshot(false)
		if err != nil {
			return facts, err
		}
		for _, info := range infos {
			if info.Role == "reviewer" {
				ids[info.ID] = true
			}
		}
	}
	for _, r := range st.Records {
		if _, known := facts.Paused[r.RoomID]; !known {
			facts.Streaks[r.RoomID] = s.expediteStreak("review:" + r.RoomID)
			paused, err := s.RoomPaused(r.RoomID)
			if err != nil {
				return facts, err
			}
			facts.Paused[r.RoomID] = paused
		}
		if !facts.Paused[r.RoomID] && needsReview(st, *r) {
			facts.Need = append(facts.Need, r)
		}
		// The pool releases a closed PR's reviewer within a pass. Older closed
		// PRs only name closed reviewers, and reading those sessions on every
		// call only to drop them was the engine's largest CPU cost.
		if (r.State == "closed" || r.State == "merged") && !r.ClosedAt.IsZero() && s.now().Sub(r.ClosedAt) > closedReviewerWindow {
			continue
		}
		if r.Reviewer != "" {
			ids[r.Reviewer] = true
		}
		if r.LastReviewer != "" {
			ids[r.LastReviewer] = true
		}
	}
	for id := range ids {
		info, turn, err := s.reviewerInfo(id)
		if err == nil && info.Role == "reviewer" && info.Status != subagent.StatusClosed {
			facts.Sessions[id] = reviewerTurn{info, turn}
		}
	}
	sort.Slice(facts.Need, func(i, j int) bool {
		a, b := facts.Need[i], facts.Need[j]
		// Passing CI gets the next slot. Other eligible heads can still be
		// reviewed in parallel with CI. Equal priority uses a stable key.
		if (a.CI == "passed") != (b.CI == "passed") {
			return a.CI == "passed"
		}
		return a.Key < b.Key
	})
	for _, r := range facts.Need {
		live, ok := facts.Sessions[r.Reviewer]
		if ok && prfacts.Busy(*r, live.Info, live.Turn) {
			facts.Busy[r.Key] = live
		}
	}

	return facts, nil
}

func (f ReviewFacts) Queue(roomID string) []*Record {
	queue, blocked := []*Record{}, []*Record{}
	for _, r := range f.Need {
		if (roomID == "" || r.RoomID == roomID) && f.Busy[r.Key].Info.ID == "" {
			if dependencyWaiting(f.State, *r) {
				blocked = append(blocked, r)
			} else {
				queue = append(queue, r)
			}
		}
	}
	return append(reviewPriority(queue, f.ExpeditedGoals, f.Streaks[roomID]), blocked...)
}

func (f ReviewFacts) ReviewState(r Record) (string, *int, string) {
	if f.Paused[r.RoomID] {
		return "blocked", nil, "Room sessions are paused."
	}

	if r.Watching && r.State == "open" && r.IsDraft {
		return "blocked", nil, "The PR is a draft."
	}
	if r.Watching && r.State == "open" && !headVerified(r) {
		return "blocked", nil, "The PR head is unverified; waiting for a successful refresh."
	}
	if r.RoundHead == r.Head {
		if approved(r.Verdict) {
			return "approved", nil, ""
		}
		if r.Verdict == "changes" || r.Verdict == "question" {
			return "changes", nil, ""
		}
	}
	if !r.Watching || r.State != "open" || EffectivePolicy(f.State, r).Review == "off" {
		return "off", nil, ""
	}
	if r.CI == "failed" {
		return "blocked", nil, "CI failed for the current head."
	}
	if f.Busy[r.Key].Info.ID != "" {
		return "reviewing", nil, ""
	}
	for i, item := range f.Queue(r.RoomID) {
		if item.Key == r.Key {
			position := i + 1
			return "queued", &position, ""
		}
	}
	return "queued", nil, ""
}

func (s *Service) ReviewerStatus(roomID string) (ReviewerStatus, error) {
	st, err := s.Store.Read()
	if err != nil {
		return ReviewerStatus{}, err
	}
	facts, err := s.ReviewFacts(st)
	if err != nil {
		return ReviewerStatus{}, err
	}
	cfg := st.Reviewers[roomID]
	out := ReviewerStatus{ExpeditedGoalID: facts.ExpeditedGoals[roomID], Reviewers: []ReviewerSlotStatus{}, Helpers: []ReviewerHelper{}, Enabled: !cfg.Disabled && !facts.Paused[roomID], ProfileSource: "auto", Queue: []ReviewQueued{}, LastError: cfg.LastError}
	for _, record := range st.Records {
		if record.RoomID != roomID {
			continue
		}
		for _, round := range record.ReviewRounds {
			out.Recent = append(out.Recent, state.ReviewResult{ChangeID: changeID(*record), PR: record.Number, Verdict: round.Verdict, At: round.At})
		}
	}
	sort.Slice(out.Recent, func(i, j int) bool { return out.Recent[i].At.After(out.Recent[j].At) })
	if len(out.Recent) > 10 {
		out.Recent = out.Recent[:10]
	}

	if out.Recent == nil {
		out.Recent = []state.ReviewResult{}
	}
	if cfg.Profile != "" {
		out.ProfileSource = "pinned"
	}
	out.Profile, out.ProfileReason, err = s.chooseProfile(cfg)
	if err != nil && out.LastError == "" {
		out.LastError = err.Error()
	}
	for _, r := range facts.Need {
		live, ok := facts.Busy[r.Key]
		if !ok || r.RoomID != roomID {
			continue
		}
		current := &ReviewCurrent{ChangeID: changeID(*r), Kind: changeKind(*r), PR: r.Number, Title: r.Title, Head: r.Head, StartedAt: r.RoundStarted}
		session := &ReviewerSession{ID: live.Info.ID, Address: live.Info.Address(), Status: live.Info.Status}
		out.Reviewers = append(out.Reviewers, ReviewerSlotStatus{Session: session, Profile: live.Info.Profile, Current: current, Status: "reviewing"})
	}
	if s.Notify != nil {
		infos, e := s.Notify.Sessions.Store.ListSnapshot(false)
		if e != nil {
			return out, e
		}
		busyIDs := map[string]bool{}
		for _, live := range facts.Busy {
			if live.Info.RoomID == roomID {
				busyIDs[live.Info.ID] = true
			}
		}
		for _, info := range infos {
			if info.RoomID == roomID && busyIDs[info.ParentSession] {
				out.Helpers = append(out.Helpers, ReviewerHelper{info.ID, info.Address(), info.Profile, info.Status})
			}
		}
	}
	for _, r := range facts.Queue(roomID) {
		out.Queue = append(out.Queue, ReviewQueued{GoalID: r.GoalID, Expedited: expeditedRecord(*r, facts.ExpeditedGoals), ChangeID: changeID(*r), Kind: changeKind(*r), PR: r.Number, Title: r.Title, Head: r.Head})
	}
	return out, nil
}

func (s *Service) validateReviewerProfile(name string) error {
	if name != "" {
		profiles, err := s.profiles()
		if err != nil {
			return err
		}
		if !profile.IsEnabled(profiles, name) {
			return fmt.Errorf("no enabled profile named %q", name)
		}
	}
	return nil
}

func (s *Service) SetReviewerProfile(roomID, name string) error {
	if err := s.validateReviewerProfile(name); err != nil {
		return err
	}
	return s.Store.update(func(st *State) error {
		cfg := st.Reviewers[roomID]
		cfg.Profile = name
		st.Reviewers[roomID] = cfg
		return nil
	})
}

func (s *Service) RestartReviewer(ctx context.Context, roomID string) error {
	lock, err := s.reviewerLock(roomID)
	if err != nil {
		return err
	}
	defer unlockReviewer(lock)
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	facts, err := s.ReviewFacts(st)
	if err != nil {
		return err
	}
	for id, live := range facts.Sessions {
		if live.Info.RoomID == roomID {
			if err = s.closeReviewer(ctx, id); err != nil {
				return err
			}
		}
	}
	return s.reconcileRoom(ctx, roomID)
}

func (s *Service) dispatchAll(ctx context.Context) error { return s.dispatch(ctx, true) }
func (s *Service) dispatch(ctx context.Context, deliver bool) error {
	st, err := s.readTickState(ctx)
	if err != nil {
		return err
	}
	facts, err := s.ReviewFacts(st)
	if err != nil {
		return err
	}
	rooms := map[string]bool{}
	for _, r := range st.Records {
		if r.Watching {
			rooms[r.RoomID] = true
		}
	}
	for _, live := range facts.Sessions {
		rooms[live.Info.RoomID] = true
	}
	var first error
	for roomID := range rooms {
		if err = ctx.Err(); err != nil {
			return errors.Join(first, err)
		}
		lock, e := s.reviewerLock(roomID)
		if e != nil {
			continue
		}
		roomCtx, cancel := context.WithTimeout(ctx, s.recordBudget())
		e = s.reconcileRoom(roomCtx, roomID)
		cancel()
		unlockReviewer(lock)
		if e != nil && first == nil {
			first = e
		}
	}
	if !deliver {
		return first
	}
	fresh, err := s.readTickState(ctx)
	if err != nil {
		return err
	}
	for key, r := range fresh.Records {
		if !hasPendingNotices(r) {
			continue
		}
		deliveryCtx, cancel := context.WithTimeout(ctx, s.recordBudget())
		err = s.flush(deliveryCtx, key)
		cancel()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Service) reviewBlocked(roomID, reason string) error {
	return s.Store.update(func(st *State) error {
		cfg := st.Reviewers[roomID]
		cfg.LastError = reason
		st.Reviewers[roomID] = cfg
		return nil
	})
}

func (s *Service) reconcileRoom(ctx context.Context, roomID string) error {
	st, err := s.readTickState(ctx)
	if err != nil {
		return err
	}
	facts, err := s.ReviewFacts(st)
	if err != nil {
		return err
	}
	busyIDs := map[string]bool{}
	busy := 0
	for _, live := range facts.Busy {
		if live.Info.RoomID == roomID {
			busyIDs[live.Info.ID] = true
			busy++
		}
	}
	failures := st.Reviewers[roomID].FailedProfiles
	retired := map[string]bool{}
	for id, live := range facts.Sessions {
		if live.Info.RoomID != roomID || busyIDs[id] {
			continue
		}
		failures = rolesession.RecordFailure(failures, live.Info, live.Turn, s.now())
		if err = s.closeReviewer(ctx, id); err != nil {
			return err
		}
		retired[id] = true
	}
	if err = s.Store.update(func(current *State) error {
		cfg := current.Reviewers[roomID]
		cfg.FailedProfiles = failures
		cfg.LastError = ""
		current.Reviewers[roomID] = cfg
		for _, r := range current.Records {
			if r.RoomID != roomID {
				continue
			}
			for id := range retired {
				retireSessionReviewNotices(r, id, "review-reconciled")
			}
			if retired[r.Reviewer] && needsReview(*current, *r) {
				rememberReviewAssignment(r)
				r.Reviewer, r.ReviewerName, r.ReviewerMode, r.RoundHead = "", "", "", ""
				r.RoundStarted = time.Time{}
			}
			if strings.HasPrefix(r.PauseReason, "review paused:") {
				r.PauseReason = ""
			}
		}
		return nil
	}); err != nil {
		return err
	}
	st, err = s.readTickState(ctx)
	if err != nil {
		return err
	}
	facts, err = s.ReviewFacts(st)
	if err != nil {
		return err
	}
	profiles, err := s.profiles()
	if err != nil {
		return s.reviewBlocked(roomID, Sanitize(err.Error()))
	}
	pending := facts.Queue(roomID)
	for len(pending) > 0 {
		i := nextReview(pending, facts.ExpeditedGoals, s.expediteStreak("review:"+roomID))
		candidate := pending[i]
		pending = append(pending[:i], pending[i+1:]...)
		if err = ctx.Err(); err != nil {
			return err
		}
		if dependencyWaiting(st, *candidate) {
			continue
		}
		cfg := st.Reviewers[roomID]
		name, _, e := s.chooseProfile(cfg)
		if e != nil {
			return s.reviewBlocked(roomID, Sanitize(e.Error()))
		}
		if busy >= reviewerProfileCap(name, profiles) {
			break
		}
		r := *candidate
		if current, e := s.reviewHeadCurrent(ctx, r); e != nil {
			return e
		} else if !current {
			continue
		}
		r.Round++
		r.RoundHead = r.Head
		r.RoundStarted = s.now()
		r.Verdict = ""
		r.Summary = ""
		r.ReviewID = 0
		r.ReviewPublishing = PublishingLocal
		r.ReviewerName = reviewerChangeName(&r)
		task, e := s.prepareReviewTask(ctx, r, "")
		if e != nil {
			return e
		}
		id, e := s.launch(ctx, r, name, task)
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return s.reviewBlocked(roomID, Sanitize(e.Error()))
		}
		if id == r.Author {
			return fmt.Errorf("author and reviewer must be different sessions")
		}
		paused, e := s.RoomPaused(roomID)
		if e != nil || paused {
			closeErr := s.closeReviewer(ctx, id)
			return errors.Join(e, closeErr)
		}
		applied := false
		if e = s.Store.update(func(current *State) error {
			live := current.Records[r.Key]
			if live == nil || current.Reviewers[roomID].Profile != cfg.Profile || current.Reviewers[roomID].ReviewPublishing != cfg.ReviewPublishing || live.Author == id || live.Head != r.Head || !needsReview(*current, *live) || dependencyWaiting(*current, *live) {
				return nil
			}
			rememberReviewAssignment(live)
			live.Round, live.RoundHead, live.RoundStarted = r.Round, r.Head, r.RoundStarted
			live.Verdict, live.Summary, live.ReviewID = "", "", 0
			live.Reviewer, live.ReviewerName, live.ReviewerMode = id, r.ReviewerName, "session"
			live.ReviewPublishing = r.ReviewPublishing
			live.PauseReason = ""
			rememberReviewAssignment(live)
			applied = true
			return nil
		}); e != nil {
			return e
		}
		if !applied {
			if e = s.closeReviewer(ctx, id); e != nil {
				return e
			}
			continue
		}
		s.expediteGranted("review:"+roomID, expeditedRecord(r, facts.ExpeditedGoals))
		busy++
	}
	return nil
}

func (s *Service) cleanupEndedRoom(ctx context.Context, roomID string) error {
	lock, err := s.reviewerLock(roomID)
	if err != nil {
		log.Printf("reviewer cleanup is deferred for room %s: %v", roomID, err)
		return nil
	}
	defer unlockReviewer(lock)
	return s.reconcileRoom(ctx, roomID)
}

func dependencyWaiting(st State, r Record) bool {
	for _, id := range r.Dependencies {
		for _, base := range st.Records {
			if (base.Key == id || changeID(*base) == id) && base.State == "open" && !prfacts.ApprovedAtHead(*base) {
				return true
			}
		}
	}
	return false
}

const defaultReviewerPoolSize = 3

func reviewerProfileCap(name string, profiles []profile.Profile) int {
	for _, p := range profiles {
		if p.Name == name && p.MaxConcurrent > 0 {
			return p.MaxConcurrent
		}
	}
	return defaultReviewerPoolSize
}
