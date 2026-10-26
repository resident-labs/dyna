package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/escalate"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/notify"
	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/subagent"
	"path/filepath"
)

type Service struct {
	// beforeCloseTransfer lets a test act after the notice scan, before the transfer commits.
	beforeCloseTransfer func(key string)
	tickReads           *tickReadView
	// RunNodeCI runs one admitted job on a machine. LocalCIRegistry holds the
	// saved results, machines and CI mode that both a local head and a pull
	// request head use.
	LocalCIRegistry localCIRegistry
	RunNodeCI       func(context.Context, localci.Node, localci.Request) (localci.Result, error)
	registeredRuns  *registeredCIRuns
	// RunningCIChanged tells clients that the local CI runs in flight changed,
	// so they read the machine view again. nil when no client hears of them.
	RunningCIChanged func()

	autoTrackFailures *sync.Map // PR key and remote head; only failed attempts remain.
	ReviewerProfiles  func() ([]profile.Profile, error)
	ReviewerInfo      func(string) (subagent.Info, subagent.Turn, error)
	CloseReviewer     func(context.Context, string) error

	Triage Triager
	Store  *Store
	GH     GitHub
	Notify *notify.Service
	Now    func() time.Time
	// Wait pauses before a retry that depends on time passing, such as a fetch of
	// a ref GitHub has not published yet. A test replaces it, so no test sleeps.
	Wait           func(context.Context, time.Duration) error
	Deliver        func(context.Context, Record, Notice) (string, error)
	noticeTimeout  time.Duration
	recordTimeout  time.Duration
	tickTimeout    time.Duration
	LaunchReviewer func(context.Context, Record, string, string, string) (string, error)
	// StartAgent starts a managed one-off agent; nil uses Notify.Sessions.Start.
	StartAgent    escalate.Start
	CleanupMerged func(context.Context, Record, string) error
	// WarmStore remembers the default branch commit each machine last warmed.
	// Nil uses the local CI store.
	WarmStore warmStore
	// WarmDefinition reads the definition a warm run takes its job from, and
	// WarmRun runs one warm run. Nil uses the local CI runner.
	WarmDefinition  func(context.Context, string, string) (*localci.Definition, error)
	WarmRun         func(context.Context, localci.Node, localci.WarmRequest) error
	registeredWarms *warmRuns
}

func New() *Service { return &Service{Store: DefaultStore(), Notify: notify.Default(), Now: time.Now} }
func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) githubFor(ref Ref) GitHub {
	if r, err := s.Store.Get(ref.Key()); err == nil {
		return s.github(r.RoomID)
	}
	return s.github("")
}

// github binds repository calls to the record's room without mutating the shared client.
func (s *Service) github(roomID string) GitHub {
	g := s.GH
	g.RoomID = roomID
	if s.Store != nil {
		g.recoverPolling = s.recoverPolling
	}
	if s.Notify != nil {
		g.Rooms = s.Notify.Rooms
	}
	return g
}
func (s *Service) Resolve(ctx context.Context, text, dir string) (Ref, error) {
	// Numbers belong to the current repository. A URL tracked from this
	// directory can name another repository with the very same PR number.
	return s.GH.Resolve(ctx, text, dir)
}
func (s *Service) Track(ctx context.Context, actor authority.Actor, ref Ref, author subagent.Info, lookupDir string) (Record, bool, error) {
	return s.TrackWithPolicy(ctx, actor, ref, author, lookupDir, PolicyOverride{}, author.ID)
}
func (s *Service) TrackWithPolicy(ctx context.Context, actor authority.Actor, ref Ref, author subagent.Info, lookupDir string, override PolicyOverride, checkedAuthor string) (Record, bool, error) {
	if err := authority.Check("git pr track", actor, nil); err != nil {
		return Record{}, false, err
	}
	override.Reason = Sanitize(override.Reason)
	if err := changepolicy.ValidateOverride(override); err != nil {
		return Record{}, false, err
	}
	st, err := s.Store.Read()
	if err != nil {
		return Record{}, false, err
	}
	if old := st.Records[ref.Key()]; old != nil {
		// A run-owned caller of the same controller first takes over the PR its
		// earlier run author left (issue D-252), so this existing-record path
		// returns a change the caller may act on.
		if moved, err := s.takeoverRunChange(ctx, author, lookupDir, func(r Record) bool { return r.Key == ref.Key() }); err != nil {
			return Record{}, false, err
		} else if moved {
			if st, err = s.Store.Read(); err != nil {
				return Record{}, false, err
			}
			if old = st.Records[ref.Key()]; old == nil {
				return Record{}, false, fmt.Errorf("PR is no longer tracked")
			}
		}
		if path := matchingCheckout(ctx, *old, lookupDir); path != "" {
			if err := s.Store.update(func(live *State) error {
				current := live.Records[ref.Key()]
				if current == nil {
					return fmt.Errorf("PR is no longer tracked")
				}
				current.RepoPath = path
				return nil
			}); err != nil {
				return Record{}, false, err
			}
			old.RepoPath = path
		}
		if !override.Empty() {
			// Use the author its caller checked, even after a concurrent transfer.
			if err := s.SetChangePolicyOverride(old.Key, checkedAuthor, override); err != nil {
				return Record{}, false, err
			}
			updated, err := s.Store.Get(old.Key)
			return updated, false, err
		}
		return *old, false, nil
	}
	// An author with no ID is root, the room owner; its notices go to the owner.
	if author.RoomID == "" {
		return Record{}, false, fmt.Errorf("an author room is required")
	}
	snap, err := s.github(author.RoomID).Snapshot(ctx, ref)
	if err != nil {
		return Record{}, false, err
	}
	rec := Record{PolicyOverride: override, Key: ref.Key(), Host: ref.Host, Repo: ref.Repo, Number: ref.Number, URL: ref.URL(), Author: author.ID, Parent: author.ParentSession, Controller: s.changeController(author), RoomID: author.RoomID, Dir: author.Dir, LookupDir: lookupDir, Head: snap.Pull.Head.SHA, State: "open", Watching: true, Created: s.now(), HeadSeen: s.now(), HeadVerifiedAt: s.now(), Seen: map[string]string{}}
	rec.Kind = "github"
	rec.ChangeID = rec.Key
	rec.RepoPath = matchingCheckout(ctx, rec, lookupDir)
	rec.BaseRef = snap.Pull.Base.Ref
	rec.IsDraft = snap.Pull.Draft
	rec.Branch, rec.Base = snap.Pull.Head.Ref, snap.Pull.Base.SHA
	rec.HeadRepo = snap.Pull.Head.Repo.FullName
	if rec.Base == "" {
		rec.Base = snap.Pull.Base.Ref
	}
	rec.Title, rec.Checks = Sanitize(snap.Pull.Title), snap.CheckSummaries(ref)
	// A "Fixes D-5" line in the body links the issues this merge will close.
	rec.Fixes = state.ParseFixLines(snap.Pull.Body)
	// Old discussion is the baseline. CI is evaluated at the first watch tick.
	for _, c := range snap.Comments {
		rec.Seen[commentKey(c)] = commentVersion(c)
	}
	if snap.Pull.State == "closed" {
		endRecord(&rec, snap.Pull.Merged, s.now())
		rec.Watching = false
		rec.PauseReason = "PR is " + rec.State
	}
	// A run-owned caller of the same controller first takes over the local
	// change its earlier run author left on this branch (issue D-252), so the
	// attach checks below read state that already names it as the author.
	if moved, err := s.takeoverRunChange(ctx, author, lookupDir, func(old Record) bool {
		return old.Kind == "local" && old.State == "open" && old.Branch == rec.Branch &&
			(old.RepoPath == rec.RepoPath || matchingCheckout(ctx, rec, old.Dir) != "")
	}); err != nil {
		return Record{}, false, err
	} else if moved {
		if st, err = s.Store.Read(); err != nil {
			return Record{}, false, err
		}
	}
	// Git probes use the read snapshot, before taking the publication guard.
	// Validate the complete candidate set again under that guard below.
	localCandidate := func(r *Record) bool {
		return r.Kind == "local" && r.State == "open" && r.Branch != "" && r.Branch == rec.Branch
	}
	checkoutMatches := map[string]bool{}
	for key, old := range st.Records {
		if localCandidate(old) {
			checkoutMatches[key] = old.RepoPath == rec.RepoPath || matchingCheckout(ctx, rec, old.Dir) != ""
		}
	}
	created := false
	err = s.Store.update(func(live *State) error {
		if old := live.Records[ref.Key()]; old != nil {
			if rec.RepoPath != "" {
				old.RepoPath = rec.RepoPath
			}
			rec = *old
			return nil
		}
		candidates := 0
		for key, old := range live.Records {
			if !localCandidate(old) {
				continue
			}
			if _, prepared := checkoutMatches[key]; !prepared || !sameLocalAttachment(st.Records[key], old) {
				return fmt.Errorf("local change changed while checking its checkout; retry tracking")
			}
			if checkoutMatches[key] && (old.RoomID != rec.RoomID || old.Author != rec.Author) {
				return fmt.Errorf("this branch belongs to another room or author")
			}
			candidates++
		}
		if candidates != len(checkoutMatches) {
			return fmt.Errorf("local change changed while checking its checkout; retry tracking")
		}
		for oldKey, matches := range checkoutMatches {
			if matches {
				old := live.Records[oldKey]
				attached := *old
				attached.RepoPath = rec.RepoPath
				attached.Key, attached.Kind, attached.Host, attached.Repo, attached.Number, attached.URL = rec.Key, "github", rec.Host, rec.Repo, rec.Number, rec.URL
				if !override.Empty() {
					attached.PolicyOverride = override
				}
				attached.HeadStale, attached.HeadVerifiedAt, attached.Error = false, rec.HeadVerifiedAt, ""
				attached.IsDraft = rec.IsDraft
				attached.Checks = rec.Checks
				attached.Seen = rec.Seen
				if attached.Head != rec.Head {
					resetReview(&attached, rec.Head)
				}
				rec = attached
				delete(live.Records, oldKey)
				break
			}
		}
		check := sessionlife.Check
		if rec.State == "open" && rec.Watching {
			check = sessionlife.Activate
		}
		if err := check(filepath.Join(filepath.Dir(s.Store.Root), "sessions"), author.ID); err != nil {
			return err
		}
		rec.BotReviews = snap.BotReviews(EffectivePolicy(*live, rec).RequiredChecks...)
		live.Records[rec.Key] = &rec
		created = true
		return nil
	})
	return rec, created, err
}

// sameLocalAttachment keeps a prepared checkout match tied to the local work
// that was inspected. Unrelated updates, such as author notes, stay live.
func sameLocalAttachment(a, b *Record) bool {
	return a != nil && b != nil &&
		a.Key == b.Key && a.ChangeID == b.ChangeID && a.Kind == b.Kind && a.State == b.State &&
		a.Branch == b.Branch && a.Head == b.Head && a.RepoPath == b.RepoPath && a.Dir == b.Dir &&
		a.RoomID == b.RoomID && a.Author == b.Author
}

// Configure changes the reviewer role while preserving publishing choices.
func (s *Service) Configure(roomID string, r Reviewer) error {
	return s.SetReviewerProfile(roomID, r.Profile)
}

// Publishing reads the effective room policy without creating a store.
func (s *Service) Publishing(roomID string) (string, error) {
	st, err := s.Store.Read()
	if err != nil {
		return "", err
	}
	return PublishingMode(st.Reviewers[roomID].ReviewPublishing), nil
}

// ConfigurePublishing preserves the reviewer and applies to future assignments.
func (s *Service) ConfigurePublishing(roomID, value string) error {
	if err := validatePublishing(value); err != nil {
		return err
	}
	return s.Store.update(func(st *State) error {
		cfg := st.Reviewers[roomID]
		cfg.ReviewPublishing = PublishingMode(value)
		st.Reviewers[roomID] = cfg
		return nil
	})
}

// ConfigureRoom atomically changes the reviewer and publishing for a room settings form.
func (s *Service) ConfigureRoom(roomID string, cfg Reviewer) error {
	if err := validatePublishing(cfg.ReviewPublishing); err != nil {
		return err
	}
	if err := s.validateReviewerProfile(cfg.Profile); err != nil {
		return err
	}
	return s.Store.update(func(st *State) error {
		current := st.Reviewers[roomID]
		current.Profile = cfg.Profile
		current.ReviewPublishing = PublishingMode(cfg.ReviewPublishing)
		st.Reviewers[roomID] = current
		return nil
	})
}

func (s *Service) Untrack(ctx context.Context, key string) error {
	err := s.Store.update(func(st *State) error {
		r := st.Records[key]
		if r == nil {
			return fmt.Errorf("PR is not tracked")
		}
		r.Watching = false
		r.PauseReason = "untracked by the owner or agent"
		return nil
	})
	if err != nil {
		return err
	}
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	return errors.Join(s.cleanupEndedRoom(ctx, r.RoomID), s.refreshUnwatched(ctx, key, true, nil))
}
func (s *Service) Resume(key string) error {
	return s.Store.update(func(st *State) error {
		r := st.Records[key]
		if r == nil {
			return fmt.Errorf("PR is not tracked")
		}
		if r.State != "open" {
			return fmt.Errorf("PR is %s", r.State)
		}
		r.Watching = true
		r.CIPaused = false
		r.Failures = 0
		r.Polling.Next = time.Time{}
		r.Polling.FullChecked = time.Time{}
		r.CIKey = ""
		r.PauseReason = ""
		return nil
	})
}
func commentKey(c Comment) string     { return fmt.Sprintf("%s:%d", c.Kind, c.ID) }
func commentVersion(c Comment) string { return digest(c.Body + c.Updated + c.Submitted + c.State) }

// commentOwner resolves the Dyna session that posted a GitHub comment. The
// comment IDs stored when Dyna posted win, and comments from a known review
// belong to its reviewer.
func commentOwner(owned map[string]string, c Comment) string {
	if session := owned[commentKey(c)]; session != "" {
		return session
	}
	if c.ReviewID > 0 {
		if session := owned[fmt.Sprintf("reviews:%d", c.ReviewID)]; session != "" {
			return session
		}
	}
	return ""
}

// claimComment records which session posted a GitHub comment or review, so
// replies and review ownership route by the stored comment ID and no marker
// needs to appear in the comment body. Root (no session) stays unattributed,
// which routes as owner feedback.
func claimComment(st *State, kind string, id int64, session string) {
	if session == "" {
		return
	}
	if st.CommentSessions == nil {
		st.CommentSessions = map[string]string{}
	}
	st.CommentSessions[fmt.Sprintf("%s:%d", kind, id)] = session
}

// submittedAfterRound reports whether a review was submitted after its round
// was assigned, so a reviewer cannot claim the owner's earlier review on the
// same head. GitHub timestamps without a value are not a ground to refuse.
func submittedAfterRound(r Record, c Comment) bool {
	if r.RoundStarted.IsZero() {
		return true
	}
	stamp := c.Submitted
	if stamp == "" {
		stamp = c.Created
	}
	if strings.TrimSpace(stamp) == "" {
		return true
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return true
	}
	// One minute absorbs clock skew between GitHub and this host.
	return !at.Before(r.RoundStarted.Add(-time.Minute))
}

// A fresh head can wait for checks to register. Silent CI stops blocking review
// after five minutes, including when workflow inspection is unavailable.
func (s *Service) ciStatus(snap Snapshot, r Record, policy ChangePolicy) (state, key string, jobs []string, runs []Run) {
	state, key, jobs, runs = snap.CIStatus(policy.RequiredChecks...)
	if reason, token := budgetFailure(r, snap, policy); reason != "" {
		if state == "failed" && strings.HasPrefix(reason, "tests over ") {
			return state, key, append(jobs, reason), runs
		}
		return "failed", "budget:" + snap.Pull.Head.SHA + ":" + token, []string{reason}, nil
	}
	if !snap.hasRealCI(policy.RequiredChecks...) && r.Head == snap.Pull.Head.SHA && !r.HeadSeen.IsZero() && !s.now().Before(r.HeadSeen.Add(5*time.Minute)) {
		state = "absent"
	}
	return
}

// retargetWait bounds the wait for a parent PR's merge to be recorded.
const retargetWait = 10 * time.Minute

// baseMerged reports whether a PR's old base reached newBase: a tracked PR
// whose final head is exactly the old base commit merged into newBase, by any
// merge method. Matching the commit, not only the branch, rejects a reused
// branch with new commits. It asks the caller to wait while a PR from that
// branch is open and has not been polled since the retarget was first seen,
// for at most retargetWait.
func baseMerged(st *State, r *Record, newBase string, seen, now time.Time) (merged, wait bool) {
	for _, p := range st.Records {
		if p == r || p.Host != r.Host || p.Repo != r.Repo || p.Branch != r.BaseRef || changeKind(*p) != "github" {
			continue
		}
		if p.State == "merged" && p.BaseRef == newBase && p.Head != "" && p.Head == r.Base {
			return true, false
		}
		if p.State == "open" && p.Checked.Before(seen) && now.Before(seen.Add(retargetWait)) {
			wait = true
		}
	}
	return false, wait
}

// budgetsDue means CI and test budget checks for this snapshot have not run
// yet. Passing or absent CI then does not clear the failure streak, because a budget
// failure may follow in the same tick.
func (s *Service) process(ctx context.Context, key string, snap Snapshot, ci, ciKey string, jobs, logs []string, budgetsDue bool, pollTime ...time.Time) error {
	// Take the per-PR routing gate before state.lock. A pending publication
	// has no routing ID yet; leave this snapshot unprocessed and retry later.
	unlock, owned, err := s.tryPublicationGate(key)
	if err != nil {
		return err
	}
	if !owned {
		return nil
	}
	defer unlock()
	if err := s.retargetObservedMerge(ctx, key, snap.Pull); err != nil {
		return err
	}
	var out processEffects
	err = s.Store.updateRecordOrAll(key, func(st *State) bool {
		r := st.Records[key]
		// A base change can need the current parent record to retain a review.
		return r != nil && snap.Pull.Base.Ref != "" && snap.Pull.Base.Ref != r.BaseRef
	}, func(st *State) error {
		r := st.Records[key]
		if r == nil || !r.Watching || (snap.headConfirmed && r.Head != snap.Pull.Head.SHA) {
			return nil
		}
		r.Title = Sanitize(snap.Pull.Title)
		r.Fixes = state.MergeFixes(r.Fixes, state.ParseFixLines(snap.Pull.Body))
		r.HeadRepo = snap.Pull.Head.Repo.FullName
		baseRetargeted, keepReview := s.observeBase(st, r, snap, pollTime)
		previousPullHead := s.observePoll(st, r, snap, pollTime)
		n := newPollNotices(r)
		if baseRetargeted && !keepReview {
			resetReview(r, r.Head)
			n.changes = append(n.changes, "base:"+r.BaseRef)
		}
		applyHead(r, snap, previousPullHead, n, &out)
		if snap.Pull.State == "closed" {
			s.closeRecord(r, snap, n, &out)
		} else {
			s.applyCI(st, r, snap, ci, ciKey, jobs, logs, budgetsDue, n)
			noticeIfLandable(*st, r, snap.GitHubFacts(*r, EffectivePolicy(*st, *r).RequiredChecks...))
			s.routeComments(st, r, snap, n)
		}
		n.deliver(r)
		return nil
	})
	if err != nil {
		return err
	}
	return s.finishProcess(ctx, key, snap, out)
}

// processEffects carries the work that runs after the state update commits.
type processEffects struct {
	merged, cleanup *Record
	cleanupHead     string
}

// pollNotices collects one poll's notices per target and the changes that
// make up their delivery token. It also keeps the typed facts of the most
// important event of each target, so the post is a typed PR event.
type pollNotices struct {
	batches, informational, batchLogs map[string][]string
	// CI results are kept apart so a CI-only notice can be marked "ci".
	ciBatches map[string][]string
	// facts holds the typed event facts of each target.
	facts   map[string]noticeFacts
	drops   map[string]int
	changes []string
}

func newPollNotices(r *Record) *pollNotices {
	n := &pollNotices{batches: map[string][]string{}, informational: map[string][]string{}, batchLogs: map[string][]string{}, ciBatches: map[string][]string{}, facts: map[string]noticeFacts{}, drops: r.PendingDrops}
	if n.drops == nil {
		n.drops = map[string]int{}
	}
	return n
}

// eventPriority orders the event types, heaviest last, so one post shows its
// most important fact.
var eventPriority = []room.PRType{
	room.PRUpdate, room.PRComment, room.PRReady, room.PRApproved, room.PRChanges,
	room.PRMerged, room.PRClosed, room.PRCIFailed, room.PRCIPaused,
}

func priority(typ room.PRType) int {
	for i, known := range eventPriority {
		if known == typ {
			return i
		}
	}
	return 0
}

// raise records the typed facts of a target's batch, keeping the strongest
// event type and its facts.
func (n *pollNotices) raise(target string, facts noticeFacts) {
	if facts.Type == "" || priority(facts.Type) < priority(n.facts[target].Type) {
		return
	}
	n.facts[target] = facts
}

func (n *pollNotices) add(target, text string) { n.batches[target] = append(n.batches[target], text) }

func (n *pollNotices) addCI(target, text string) {
	n.ciBatches[target] = append(n.ciBatches[target], text)
}

// observeBase records the PR's branch and base. It reports whether the base
// was retargeted and whether the review survives that retarget.
func (s *Service) observeBase(st *State, r *Record, snap Snapshot, pollTime []time.Time) (baseRetargeted, keepReview bool) {
	baseRetargeted = snap.Pull.Base.Ref != "" && r.BaseRef != "" && snap.Pull.Base.Ref != r.BaseRef
	// A stacked PR moves to the default branch after its parent merges.
	// Its own commits are unchanged and the old base is now in the new
	// base, so it keeps its verdict; the land trial merge and checks cover
	// the rest. Any other retarget can add unreviewed commits.
	wait := false
	if baseRetargeted && snap.Pull.Base.Ref == snap.Pull.Base.Repo.DefaultBranch && snap.Pull.Head.SHA == r.Head {
		if r.BaseRetargetSeen.IsZero() {
			r.BaseRetargetSeen = s.now()
			if len(pollTime) > 0 {
				r.BaseRetargetSeen = pollTime[0]
			}
		}
		keepReview, wait = baseMerged(st, r, snap.Pull.Base.Ref, r.BaseRetargetSeen, s.now())
	}
	// While waiting, keep the old base and its commit so the next poll
	// decides again after the parent's merge is recorded.
	r.Branch = snap.Pull.Head.Ref
	if wait {
		return false, keepReview
	}
	r.BaseRetargetSeen = time.Time{}
	if snap.Pull.Base.Ref != "" {
		r.BaseRef = snap.Pull.Base.Ref
	}
	r.Base = snap.Pull.Base.SHA
	if r.Base == "" {
		r.Base = snap.Pull.Base.Ref
	}
	return baseRetargeted, keepReview
}

// observePoll records checks, poll timing and freshness. It returns the PR
// head that the previous poll saw.
func (s *Service) observePoll(st *State, r *Record, snap Snapshot, pollTime []time.Time) (previousPullHead string) {
	if snap.Pull.State != "closed" {
		r.Checks = savedCheckSummaries(*r, snap)
		if snap.localCI != nil && snap.localCI.BudgetNote != "" {
			notice(r, r.Author, snap.localCI.BudgetNote, "informational:ci-budget:local:"+snap.Pull.Head.SHA+":"+digest(snap.localCI.BudgetNote))
		}
		r.BotReviews = snap.BotReviews(EffectivePolicy(*st, *r).RequiredChecks...)
	}
	observe(&r.Polling, s.now(), snap, reserve(quotaFor(*st, r, s.now()), s.now()))
	if len(pollTime) > 0 && r.Polling.IntervalMinutes == 1 {
		r.Polling.Next = nextPollTime(pollTime[0], 1)
	}
	r.Polling.PullVersion = pullVersion(snap.Pull, EffectivePolicy(*st, *r))
	previousPullHead = r.Polling.PullHead
	r.Polling.PullHead = snap.Pull.Head.SHA
	r.Polling.CIVersion = snap.ciProbe
	r.Polling.Error = ""
	if !snap.ciReused {
		r.Polling.FullChecked = s.now()
	}
	if r.Head != snap.Pull.Head.SHA || r.HeadSeen.IsZero() {
		r.HeadSeen = s.now()
	}
	r.Checked = s.now()
	r.Error = ""
	r.HeadStale = false
	r.HeadVerifiedAt = s.now()
	if r.Seen == nil {
		r.Seen = map[string]string{}
	}
	return previousPullHead
}

// applyHead moves the record to a new PR head and schedules stale CI cleanup.
func applyHead(r *Record, snap Snapshot, previousPullHead string, n *pollNotices, out *processEffects) {
	changedHead := snap.Pull.Head.SHA != "" && snap.Pull.Head.SHA != r.Head
	refreshedHead := previousPullHead != "" && previousPullHead != snap.Pull.Head.SHA
	if changedHead || refreshedHead {
		if snap.Pull.State != "closed" && r.Head != "" && changeKind(*r) == "github" && r.CIStaleCleanupHead != snap.Pull.Head.SHA {
			r.CIStaleCleanupHead = snap.Pull.Head.SHA
			out.cleanupHead = snap.Pull.Head.SHA
			copy := *r
			out.cleanup = &copy
		}
	}
	if changedHead {
		rememberReviewAssignment(r)
		retireReviewNotices(r, "new-head:"+snap.Pull.Head.SHA)
		r.Head = snap.Pull.Head.SHA
		r.Landing = nil
		r.CIKey = ""
		r.RoundHead = ""
		r.RoundStarted = time.Time{}
		r.Verdict = ""
		r.Summary = ""
		r.Reviewer = ""
		r.ReviewerMode = ""
		r.ReviewerName = ""
		r.ReviewID = 0
		if !r.CIPaused {
			r.PauseReason = ""
		}
	}
	if changedHead || refreshedHead {
		n.changes = append(n.changes, "head:"+r.Head)
	}
}

// closeRecord ends watching a closed or merged PR and tells its author.
func (s *Service) closeRecord(r *Record, snap Snapshot, n *pollNotices, out *processEffects) {
	r.MergeCommit = ""
	if snap.Pull.Merged {
		r.MergeCommit = snap.Pull.MergeCommit
	}
	endRecord(r, snap.Pull.Merged, s.now())
	if snap.Pull.Merged {
		copy := *r
		out.merged = &copy
		n.informational[r.Author] = append(n.informational[r.Author], "Your PR branch worktree is merged and can be removed after this session closes, if the tree is clean.")
	}
	r.Watching = false
	if changeKind(*r) == "github" && !r.CICleanupDone {
		r.CICleanupDone = true
		copy := *r
		out.cleanup = &copy
	}
	r.PauseReason = "PR is " + r.State
	n.changes = append(n.changes, r.State)
}

const cancelledCIReason = "The last CI run was cancelled. Push or rerun CI to test this commit."

// applyCI records the CI state and its reason, counts consecutive failures,
// and queues a CI notice for the author.
func (s *Service) applyCI(st *State, r *Record, snap Snapshot, ci, ciKey string, jobs, logs []string, budgetsDue bool, n *pollNotices) {
	r.CI = ci
	r.LocalCIConfig = snap.localCIConfig
	r.CIReason, _ = budgetFailure(*r, snap, EffectivePolicy(*st, *r))
	// Cancelled CI did not test the head: it needs a push or rerun, but it
	// neither wakes the author nor counts toward the failure pause.
	cancelled := ci == "failed" && r.CIReason == "" && snap.ciOnlyCancelled(EffectivePolicy(*st, *r).RequiredChecks...)
	if cancelled {
		r.CIReason = cancelledCIReason
	}
	if ci == "absent" {
		switch {
		case snap.localCIConfig == state.LocalCIConfigNotConfigured:
			r.CIReason = "Local CI is not configured; this repository uses hosted CI only."
		case snap.NoPRWorkflows:
			r.CIReason = "This repository has no active pull request workflows."
		default:
			r.CIReason = "No CI ran for this change within 5 minutes."
		}
	}
	if ci == "pending" && snap.localCIConfig == state.LocalCIConfigHeadMissing && r.CIReason == "" {
		r.CIReason = "This head removed the local CI definition; checks required by the base definition still apply."
	}
	if ci != "pending" && !cancelled && ciKey != r.CIKey {
		r.CIKey = ciKey
		n.changes = append(n.changes, "ci:"+ciKey)
		if ci == "failed" {
			r.Failures++
			if r.Failures >= 3 {
				if !r.CIPaused {
					r.CIPaused = true
					r.PauseReason = "CI wakes paused after 3 consecutive failures; use dyna git pr resume or wait for passing CI"
					text := fmt.Sprintf("PR #%d failed CI three times in a row. Later CI failures will not wake the author. Inspect dyna git pr show %s. Failed jobs: %s.", r.Number, r.URL, strings.Join(jobs, ", "))
					n.batchLogs[r.Author] = logs
					n.raise(r.Author, noticeFacts{Type: room.PRCIPaused, Summary: strings.Join(jobs, ", ")})
					n.addCI(r.Author, text)
				}
			} else if !r.CIPaused && !strings.HasPrefix(ciKey, "budget:") {
				names := strings.Join(jobs, ", ")
				if names == "" {
					names = "failed workflow"
				}
				text := fmt.Sprintf("CI failed (%d/3): %s. Fix the failure and push to this PR.", r.Failures, names)
				n.batchLogs[r.Author] = logs
				n.raise(r.Author, noticeFacts{Type: room.PRCIFailed, Summary: names})
				n.addCI(r.Author, text)
			}
		}
	}
	if (ci == "passed" || ci == "absent") && !budgetsDue && (r.Failures > 0 || r.CIPaused) {
		r.Failures = 0
		r.CIPaused = false
		r.PauseReason = ""
	}
}

// routeComments triages new comments and routes each kept one to the author
// and, while a review runs, to the live reviewer.
func (s *Service) routeComments(st *State, r *Record, snap Snapshot, n *pollNotices) {
	policy := commentRules(*st, refRecord(*r))
	trust := digest("comment-trust-v1\n" + snap.OwnerLogin + "\n" + strings.Join(policy.TrustedBots, "\n"))
	trustChanged := r.CommentTrust != trust
	if trustChanged {
		// Retire pending batches made under the previous trust policy. The
		// current snapshot below replaces them after applying the new gate.
		for i := range r.Notices {
			old := &r.Notices[i]
			if !old.Done && old.Kind != "held-comment" && strings.Contains(old.Text, "GitHub ") {
				old.SupersededBy = "comment-trust-policy"
			}
		}
	}
	r.CommentTrust = trust
	var events []Event
	for _, c := range snap.Comments {
		k, v := commentKey(c), commentVersion(c)
		if r.Seen[k] == v && (!trustChanged || r.CommentDecisions[k].Reason == "owner_dismissed") {
			continue
		}
		r.Seen[k] = v
		n.changes = append(n.changes, k+":"+v)
		c.Body = Sanitize(c.Body)
		events = append(events, Event{ID: k, Kind: c.Kind, Commenter: c.User.Login, IsBot: isBot(c.User), Text: c.Body, ReviewState: c.State, Association: c.Association, Session: commentOwner(st.CommentSessions, c)})
	}
	pr := PRContext{URL: r.URL, AuthorSession: r.Author, FormerAuthors: r.FormerAuthors, OwnerLogin: snap.OwnerLogin}
	r.OwnerLogin = snap.OwnerLogin
	rules := policy.Triage(pr, events)
	decisions := rules
	if s.Triage != nil && len(events) > 0 {
		decisions = s.Triage.Triage(pr, events)
		if len(decisions) != len(events) {
			decisions = rules
		}
	}
	// New comments steer a live reviewer; its final review answers them.
	info, turn, infoErr := s.reviewerInfo(r.Reviewer)
	liveReviewer := infoErr == nil && needsReview(*st, *r) && prfacts.Busy(*r, info, turn)
	var reviewerTexts []string
	for i, e := range events {
		decision := decisions[i]
		if rules[i].Reason == "owner" || rules[i].Action == "drop" || rules[i].Action == "hold" {
			decision = rules[i]
		}
		if decision.Action == "hold" && r.AdmittedComments[e.ID] == r.Seen[e.ID] {
			decision = Decision{Action: "keep", Reason: "owner_admitted"}
		}
		if decision.Action != "drop" && decision.Action != "hold" {
			decision.Action = "keep"
		}
		if r.CommentDecisions == nil {
			r.CommentDecisions = map[string]Decision{}
		}
		if decision.Reason == "owner" && rules[i].Reason != "owner" {
			decision.Reason = "trusted_comment"
		}
		decision.Reason = Sanitize(clip(decision.Reason, 300))
		r.CommentDecisions[e.ID] = decision
		if decision.Action == "hold" {
			if r.HeldComments == nil {
				r.HeldComments = map[string]state.HeldComment{}
			}
			r.HeldComments[e.ID] = state.HeldComment{ID: e.ID, Commenter: e.Commenter, Body: e.Text, URL: commentURL(snap, e.ID), Version: r.Seen[e.ID]}
			r.Notices = append(r.Notices, Notice{ID: "msg_" + digest(r.Key + "/held/" + e.ID + "/" + r.Seen[e.ID])[:16], Kind: "held-comment", CommentID: e.ID, Text: fmt.Sprintf("PR #%d: Held: outside commenter. Review this comment in Dyna to admit or dismiss it.", r.Number)})
			continue
		}
		delete(r.HeldComments, e.ID)
		if decision.Action == "drop" {
			n.drops[decision.Reason]++
			if id, ok := strings.CutPrefix(decision.Reason, "learned_noise:"); ok {
				if rule, exists := st.NoiseRules[id]; exists {
					rule.Hits++
					rule.LastMatched = s.now()
					st.NoiseRules[id] = rule
				}
			}
		}
		toReviewer := liveReviewer && e.Session != r.Reviewer && reviewerHears(decision)
		if decision.Action == "drop" && !toReviewer {
			continue
		}
		text := commentNotice(e, decision, commentLink(snap, e.ID))
		if decision.Action != "drop" {
			n.raise(r.Author, noticeFacts{Type: room.PRComment, Commenter: e.Commenter, Summary: fmt.Sprintf("new %s from %s", e.Kind, e.Commenter)})
			n.add(r.Author, text)
		}
		if toReviewer {
			reviewerTexts = append(reviewerTexts, text)
		}
	}
	// A system notice never falls back to the owner when the reviewer is gone.
	if len(reviewerTexts) > 0 {
		text := fmt.Sprintf("New comments on %s while you review head %s. Keep reviewing. Answer each comment in your final review.\n\n%s", changeLabel(*r), r.Head, strings.Join(reviewerTexts, "\n\n"))
		systemReviewNotice(r, r.Reviewer, text, "review-comments:"+digest(strings.Join(reviewerTexts, ";")))
	}
}

func commentURL(snap Snapshot, id string) string {
	for _, c := range snap.Comments {
		if commentKey(c) == id {
			return c.URL
		}
	}
	return ""
}

// commentLink is a comment's URL, after its file and line for a code comment.
func commentLink(snap Snapshot, id string) string {
	for _, c := range snap.Comments {
		if commentKey(c) != id {
			continue
		}
		if c.Path == "" {
			return c.URL
		}
		line := c.Line
		if line == 0 {
			line = c.OriginalLine
		}
		return fmt.Sprintf("%s:%d\n%s", c.Path, line, c.URL)
	}
	return ""
}

// deliver queues one notice per target per poll, so the author wakes once.
// A notice with only CI results is kind "ci" and is dropped if the change
// ends before delivery; a notice with a comment is always kept.
func (n *pollNotices) deliver(r *Record) {
	sort.Strings(n.changes)
	token := digest(strings.Join(n.changes, ";"))
	targets := map[string]bool{}
	for target := range n.batches {
		targets[target] = true
	}
	for target := range n.ciBatches {
		targets[target] = true
	}
	for target := range n.informational {
		targets[target] = true
	}
	for target := range targets {
		texts := append(append([]string{}, n.batches[target]...), n.ciBatches[target]...)
		texts = append(texts, n.informational[target]...)
		text := strings.Join(texts, "\n\n")
		if len(n.batchLogs[target]) > 0 {
			text += "\n\nLog tail:\n" + strings.Join(n.batchLogs[target], "\n\n")
		}
		kindToken := token
		if len(n.batches[target]) == 0 {
			kindToken = "ci:" + token
			if len(n.ciBatches[target]) == 0 {
				kindToken = "informational:" + token
			}
		}
		facts := n.facts[target]
		if facts.Type == "" {
			facts.Type = typeOfToken(*r, kindToken)
		}
		noticeTyped(r, target, text, kindToken, facts, n.drops)
	}
	r.PendingDrops = n.drops
	if len(n.batches) > 0 || len(n.ciBatches) > 0 || len(n.informational) > 0 {
		r.PendingDrops = nil
	}
}

// finishProcess runs the effects of a committed update: CI run cleanup, room
// cleanup for an ended PR, and the merge hook and branch cleanup.
func (s *Service) finishProcess(ctx context.Context, key string, snap Snapshot, out processEffects) error {
	if out.cleanup != nil {
		s.cleanupCIRuns(ctx, *out.cleanup, out.cleanupHead)
	}
	if snap.Pull.State == "closed" {
		r, err := s.Store.Get(key)
		if err != nil {
			return err
		}
		if err := s.cleanupEndedRoom(ctx, r.RoomID); err != nil {
			return err
		}
	}
	if out.merged == nil {
		return nil
	}
	repo := out.merged.LookupDir
	if repo == "" {
		repo = out.merged.Dir
	}
	_ = hooks.New().Emit(ctx, hooks.Event{Event: hooks.PRMerged, Repo: repo, Worktree: repo, Branch: snap.Pull.Head.Ref, SHA: snap.Pull.Head.SHA, PR: fmt.Sprint(snap.Pull.Number), Session: out.merged.Author, Room: out.merged.RoomID})
	// The merged PR closes the issues it names, whichever trigger saw the merge.
	issueErr := s.closeFixedIssues(ctx, *out.merged, out.merged.MergeCommit)
	if snap.Pull.Head.Ref == "" {
		return issueErr
	}
	if s.CleanupMerged != nil {
		return errors.Join(issueErr, s.CleanupMerged(ctx, *out.merged, snap.Pull.Head.Ref))
	}
	_, err := maintenance.CleanBranch(ctx, []string{repo}, snap.Pull.Head.Ref)
	return errors.Join(issueErr, err)
}

// ReviewAssignment is independent of publishing and can later describe local branches.
type ReviewAssignment struct {
	ChangeID string      `json:"changeId"`
	Kind     string      `json:"kind"`
	Repo     string      `json:"repo"`
	Branch   string      `json:"branch"`
	Base     string      `json:"base"`
	Head     string      `json:"head"`
	URL      string      `json:"url,omitempty"`
	Notes    ChangeNotes `json:"notes"`
}

func assignment(r Record) ReviewAssignment {
	return ReviewAssignment{ChangeID: changeID(r), Kind: changeKind(r), Repo: r.Repo, Branch: r.Branch, Base: r.Base, Head: r.Head, URL: r.URL, Notes: r.Notes}
}
func reviewTask(r Record) string {
	base := fmt.Sprintf("Review %s at head %s. Do not merge or push changes.\n", changeLocation(r), r.Head)
	command := "dyna git change verdict " + shellquote.Quote(changeID(r))
	if changeKind(r) == "github" {
		base += "Read the diff with gh pr diff.\n"
		command = "dyna git pr verdict " + shellquote.Quote(r.URL)
	}
	if len(r.ReviewRounds) > 0 {
		base += "This is an incremental review. Confirm earlier findings and replies against the delta.\n"
	}
	input, _ := json.Marshal(assignment(r))
	base += "Assignment input: " + string(input) + "\n"
	base += "You may start at most 3 live helper sessions, one level deep, using your own profile or a cheaper profile (higher cost-efficiency). Helpers report findings to you. Only you may record this assignment's verdict. Keep this reviewer session open between reviews.\n"
	return base + fmt.Sprintf(`This assignment is Dyna-only. Read GitHub without posting: do not publish GitHub reviews or comments for this assignment.
Record exactly one typed result in Dyna before your turn ends: %s approve|changes|approve-fast|stop-early --body-file <file> --findings-file <json-file> --head %s. A turn that ends without a typed result is an error. Use changes when fixes are needed, with structured findings. approve-fast is for test code or low-risk changes of any kind, at the reviewer's own discretion. It lands without a merge trial when required CI passed at exactly the approved head. Use stop-early with --reason superseded|duplicate|empty|too-large when you stop before judging the change. Reviews over 12000 bytes are rejected, never silently clipped. Include findings, file paths, line numbers and suggested fixes in the Markdown. No GitHub review ID is needed. The stored result is visible in Dyna and a changes result is delivered to the author.`, command, r.Head)
}
func (s *Service) launch(ctx context.Context, r Record, profile, task string) (string, error) {
	if s.LaunchReviewer != nil {
		return s.LaunchReviewer(ctx, r, r.ReviewerName, profile, task)
	}
	dir, err := s.reviewerDir(r)
	if err != nil {
		return "", err
	}
	m := s.Notify.Sessions

	info, _, err := m.StartForEngine(subagent.StartOptions{Service: true, Profile: profile, Name: r.ReviewerName, Role: "reviewer", Room: r.RoomID, Dir: dir,
		Goal: fmt.Sprintf("Review %s at the assigned head; done when its Dyna verdict is recorded.", changeLabel(r)), Task: task})
	return info.ID, err
}

// reviewerDir is the checkout a new reviewer session starts in. A record keeps
// the author's worktree path, and closing that author's session removes the
// worktree, so a reviewer never starts in a path that is gone. A reviewer reads
// the assigned head from git, so any checkout of the repository serves, and the
// room repository is the one checkout that outlives the sessions.
func (s *Service) reviewerDir(r Record) (string, error) {
	roomRepo := ""
	if s.Notify != nil && s.Notify.Rooms != nil {
		if rec, err := s.Notify.Rooms.Resolve(r.RoomID); err == nil {
			roomRepo = rec.Repo
		}
	}
	for _, dir := range []string{r.RepoPath, r.LookupDir, r.Dir, roomRepo} {
		if st, err := os.Stat(dir); dir != "" && err == nil && st.IsDir() {
			return dir, nil
		}
	}
	return "", checkoutError(r)
}
func (s *Service) Verdict(ctx context.Context, key, actor, head string, verdict state.ReviewVerdict, summary string, reviewID int64) error {
	return s.RecordReviewResult(ctx, key, actor, ReviewResult{Verdict: verdict, Head: head, Summary: summary, ReviewID: reviewID})
}

// ErrStaleVerdict means an assigned review ended before its verdict arrived.
type ErrStaleVerdict struct{ Message string }

func (e *ErrStaleVerdict) Error() string { return e.Message }

func staleVerdict(r Record, actor, head string, pull Pull, reviewID int64, named bool) error {
	if !headVerified(r) {
		return fmt.Errorf("PR head is unverified; wait for a successful refresh")
	}
	if head == "" {
		return nil
	}
	assigned := named || actor != "" && ((r.Reviewer == actor && r.RoundHead == head) ||
		(r.LastReviewer == actor && r.LastReviewHead == head))
	for _, reviewer := range r.ReviewAssignments[head] {
		if actor != "" && reviewer == actor {
			assigned = true
		}
	}
	ended := !r.Watching || r.State != "open" || pull.State == "closed"
	changed := head != "" && (head != r.Head || (!named && head != r.RoundHead) || (pull.Head.SHA != "" && pull.Head.SHA != head))
	if !ended && !changed {
		return nil
	}
	if !assigned {
		return fmt.Errorf("this session is not the assigned reviewer for this head")
	}
	message := fmt.Sprintf("%s has a newer head; your new assignment follows", changeLabel(r))
	if ended {
		state := r.State
		if pull.Merged {
			state = "merged"
		} else if state == "open" {
			state = "closed"
		}
		message = fmt.Sprintf("%s already %s; no verdict needed", changeLabel(r), state)
	}
	if reviewID > 0 {
		message += "; your published review remains on GitHub"
	}
	return &ErrStaleVerdict{Message: message}
}

func (s *Service) VerdictWithFindings(ctx context.Context, key, actor, head string, verdict state.ReviewVerdict, summary string, reviewID int64, findings []Finding) error {
	return s.RecordReviewResult(ctx, key, actor, ReviewResult{Verdict: verdict, Head: head, Summary: summary, ReviewID: reviewID, Findings: findings})
}

// RecordReviewResult records one reviewer turn's typed result. It is the one
// engine call a reviewer uses, and it rejects anything that is not a registered
// result.
func (s *Service) RecordReviewResult(ctx context.Context, key, actor string, in ReviewResult) error {
	return s.reviewResult(ctx, key, actor, in, nil)
}

func (s *Service) reviewResult(ctx context.Context, key, actor string, in ReviewResult, confirmed *Snapshot) error {
	if err := prepareReviewResult(in); err != nil {
		return err
	}
	reviewID, head, summary, verdict := in.ReviewID, in.Head, in.Summary, in.Verdict
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	summary = Sanitize(summary)
	in.Summary = summary
	key = r.Key
	policyState, err := s.Store.Read()
	if err != nil {
		return err
	}
	named := s.namedReviewer(policyState, r, actor) || (confirmed != nil && deferredReplayReviewer(r, actor, head))
	paused := confirmed == nil && changeKind(r) == "github" && s.now().Before(quotaFor(policyState, &r, s.now()).PauseUntil)
	if !paused {
		if err := staleVerdict(r, actor, head, Pull{}, reviewID, named); err != nil {
			return err
		}
	}
	if named {
		if err := validateVerdictReviewer(r, actor, head, reviewID, named); err != nil {
			return err
		}
	}
	policy := EffectivePolicy(policyState, r)
	if policy.Review == "off" && !named {
		return fmt.Errorf("review is off for this change")
	}
	if changeKind(r) == "local" {
		r.ReviewPublishing = PublishingLocal
	}
	review := r
	if named && review.RoundHead != head {
		review.Round++
	}
	prepared, err := prepareFindings(review, verdict, in.Findings)
	if err != nil {
		return err
	}
	in.Findings = prepared
	if paused {
		return s.savePendingVerdict(r.Key, actor, head, in)
	}
	// A paused submission keeps the reviewer's requested result and applies its
	// publication kind when it replays.
	in = s.normalizeReviewResult(r, in)
	verdict, summary = in.Verdict, in.Summary
	prepared, err = prepareFindings(r, verdict, in.Findings)
	if err != nil {
		return err
	}
	in.Findings = prepared
	var snap Snapshot
	if confirmed != nil {
		snap = *confirmed
	} else if changeKind(r) == "github" {
		// Confirm readiness metadata through its single writer before reading fresh CI.
		// A verdict must not combine current checks with an old draft or mergeability result.
		if err := s.ConfirmHeads(ctx, r.Key); err != nil {
			return err
		}
		r, err = s.Store.Get(key)
		if err != nil {
			return err
		}
		snap, err = s.github(r.RoomID).Snapshot(ctx, refRecord(r))
		if err != nil {
			return err
		}
		if err := staleVerdict(r, actor, head, snap.Pull, reviewID, named); err != nil {
			return err
		}
	} else {
		current, e := gitRead(ctx, r.RepoPath, "rev-parse", "--verify", "--end-of-options", "refs/heads/"+r.Branch+"^{commit}")
		if e != nil {
			return e
		}
		snap.Pull.State = "open"
		snap.Pull.Head.SHA = current
	}
	pull := snap.Pull

	if head == "" {
		return fmt.Errorf("a local review must supply --head from its assignment")
	}
	var registered *Comment
	if reviewID > 0 {
		for i := range snap.Comments {
			c := &snap.Comments[i]
			if c.Kind == "reviews" && c.ID == reviewID {
				registered = c
				break
			}
		}
		if registered == nil {
			return fmt.Errorf("review ID was not found")
		}
	}

	if head == "" && registered != nil {
		head = registered.Commit
	}
	if err := staleVerdict(r, actor, head, snap.Pull, reviewID, named); err != nil {
		return err
	}
	withCI, err := s.withRegisteredCI(ctx, r, snap, policy)
	// A head whose local CI definition this engine cannot parse still gets its
	// review recorded. Merge gates read the same definition and stay closed.
	if err != nil && !errors.Is(err, localci.ErrDefinitionInvalid) {
		return err
	}
	if err == nil {
		snap = withCI
	}

	err = s.Store.update(func(st *State) error {
		r := st.Records[key]
		if r == nil {
			return fmt.Errorf("PR is not tracked")
		}
		named := s.namedReviewer(*st, *r, actor) || (confirmed != nil && deferredReplayReviewer(*r, actor, head))
		if err := staleVerdict(*r, actor, head, snap.Pull, reviewID, named); err != nil {
			return err
		}
		freshPolicy := EffectivePolicy(*st, *r)
		if freshPolicy.Review == "off" && !named {
			return fmt.Errorf("review is off for this change")
		}
		if changeKind(*r) == "github" {
			// Readiness below needs CI on this head now, not the last poll.
			r.CI, _, _, _ = s.ciStatus(snap, *r, freshPolicy)
		}
		if changeKind(*r) == "local" {
			r.ReviewPublishing = PublishingLocal
		}
		if pull.State != "open" {
			return fmt.Errorf("PR is not open and watched")
		}
		if err := validateVerdictReviewer(*r, actor, head, reviewID, named); err != nil {
			return err
		}
		if reviewID == 0 {
			r.ReviewPublishing = PublishingLocal
		}
		if head == "" {
			head = r.RoundHead
		}
		if head == "" || head != r.Head || (!named && head != r.RoundHead) || pull.Head.SHA != head {
			return fmt.Errorf("stale review: PR head changed; review the current head")
		}
		in.Head = head
		if registered != nil {
			owner := commentOwner(st.CommentSessions, *registered)
			if (owner != "" && owner != actor) || registered.Commit != head || registered.State == "PENDING" || !submittedAfterRound(*r, *registered) {
				return fmt.Errorf("review ID must name this reviewer's published review for the assigned head")
			}
			claimComment(st, registered.Kind, registered.ID, actor)
		}
		if named && r.RoundHead != head {
			r.Round++
			r.RoundHead = head
			r.Verdict, r.Summary, r.ReviewID = "", "", 0
			r.StopReason, r.FastLand = "", nil
		}
		submitted := &state.PendingVerdict{Actor: actor, Head: head, Verdict: verdict, Summary: summary, ReviewID: reviewID, Findings: prepared, StopReason: in.StopReason, FastLand: in.FastLand}
		if done, err := recordedVerdict(*r, submitted); done || err != nil {
			if done {
				removePendingVerdict(r, submitted)
			}
			return err
		}
		removePendingVerdict(r, submitted)
		r.ReviewRounds = append(r.ReviewRounds, state.ReviewRound{Reviewer: actor, ReviewID: reviewID, Round: r.Round, Head: head, Verdict: verdict, Summary: summary, Findings: cloneFindings(prepared), StopReason: in.StopReason, FastLand: in.FastLand, At: s.now()})
		// Any authorized approval satisfies this head. Further reviews keep
		// their own audit entry without erasing an existing approval.
		if verdict == state.VerdictStopEarly {
			// A stop-early judges nothing: the head still needs a review.
			r.StopReason = in.StopReason
			r.FastLand = nil
		} else if !approved(r.Verdict) {
			r.Verdict = verdict
			r.Summary = summary
			r.ReviewID = reviewID
			r.StopReason = ""
			r.FastLand = in.FastLand
			r.Findings = append(prepared, ownerFindingsOf(r.Findings)...)
		}

		target := r.Author
		detail := summary
		if verdict == state.VerdictChanges {
			for _, c := range snap.Comments {
				if commentOwner(st.CommentSessions, c) == actor && (c.Commit == "" || c.Commit == head) && c.State != "PENDING" && r.Seen[commentKey(c)] != commentVersion(c) {
					detail += "\n\n" + clip(c.Body, 4000) + "\n" + c.URL
					r.Seen[commentKey(c)] = commentVersion(c)
				}
			}
		}
		if approved(verdict) {
			target = ""
			status := "Dyna merges automatically when approved and green."
			if changeKind(*r) == "github" && (r.CI == "pending" || r.CI == "failed") {
				status = "CI has not passed on this head yet. Dyna reports ready when it passes."
			}
			detail = clip(keepLines(summary), 1500) + "\n\n" + status
		}
		if verdict == state.VerdictStopEarly {
			target = ""
			detail = fmt.Sprintf("The reviewer stopped early: %s.", in.StopReason)
			if summary != "" {
				detail += "\n\n" + clip(keepLines(summary), 1500)
			}
		}
		detail = Sanitize(detail)
		ev := reviewEventOf(*r, in, detail)
		text := fmt.Sprintf("Reviewer requests changes for %s at %s:\n%s", changeLabel(*r), head, detail)
		if approved(verdict) {
			text = fmt.Sprintf("%s at %s approved by its Dyna reviewer:\n\n%s", changeLabel(*r), head, detail)
		}
		if verdict == state.VerdictStopEarly {
			text = fmt.Sprintf("The reviewer stopped early on %s at %s:\n%s", changeLabel(*r), head, detail)
		}
		queueReviewNotice(r, target, text, fmt.Sprintf("%d:%s:%s:%s", r.Round, head, actor, verdict), ev)
		r.PendingDrops = nil
		noticeIfLandable(*st, r, snap.GitHubFacts(*r, EffectivePolicy(*st, *r).RequiredChecks...))
		return nil
	})
	if err != nil {
		return err
	}
	return s.flush(ctx, key)
}
func hasPendingNotices(r *Record) bool {
	if r == nil {
		return false
	}
	for _, n := range r.Notices {
		if !n.Done && n.SupersededBy == "" {
			return true
		}
	}
	return false
}

func (s *Service) flush(ctx context.Context, key string) error {
	r, err := s.Store.Get(key)
	if err != nil {
		return err
	}
	var first error
	timeout := s.noticeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	// Try untouched notices before failed notices so a permanent failure cannot
	// consume every delivery slot on each tick.
	sort.SliceStable(r.Notices, func(i, j int) bool {
		return r.Notices[i].Error == "" && r.Notices[j].Error != ""
	})
	attempted := 0
	for _, n := range r.Notices {
		if n.Done || n.SupersededBy != "" {
			continue
		}
		if attempted == noticesPerTick {
			break
		}
		if err := ctx.Err(); err != nil {
			if attempted == 0 {
				return err
			}
			return first
		}
		// Reload before each delivery: an earlier delivery or a concurrent
		// landing can change the record while this batch is being sent.
		st, e := s.readTickState(ctx)
		if e != nil {
			return e
		}
		live := st.Records[key]
		if live == nil {
			return first
		}
		found := false
		for _, pending := range live.Notices {
			if pending.ID == n.ID {
				n = pending
				found = true
				break
			}
		}
		if !found || n.Done || n.SupersededBy != "" {
			continue
		}
		// Keep instructions pending until the head is confirmed. Operational
		// warnings and already observed CI facts still need delivery.
		if !headVerified(*live) && (n.Kind == "land" || reviewNotice(n)) {
			continue
		}
		if staleNotice(st, *live, n) != "" {
			if e := s.Store.updateRecords([]string{key}, func(st *State) error {
				if current := st.Records[key]; current != nil {
					for i := range current.Notices {
						pending := &current.Notices[i]
						if pending.ID == n.ID && !pending.Done && pending.SupersededBy == "" {
							pending.SupersededBy = staleNotice(*st, *current, *pending)
						}
					}
				}
				return nil
			}); e != nil {
				return e
			}
			continue
		}
		r = *live
		attempted++
		// TODO: legacy/shim remove later — repair notices saved before final-body size limits existed.
		n.Text = boundNotice(n.Text)
		deliveryCtx, cancel := context.WithTimeout(ctx, timeout)
		var postID string
		if s.Deliver != nil {
			postID, err = s.Deliver(deliveryCtx, r, n)
		} else {
			postID, err = s.deliver(deliveryCtx, r, n)
		}
		cancel()
		savedErr := err
		if e := s.Store.updateRecords([]string{key}, func(st *State) error {
			r := st.Records[key]
			if r == nil {
				return nil
			}
			for i := range r.Notices {
				if r.Notices[i].ID == n.ID {
					r.Notices[i].Text = n.Text
					r.Notices[i].PostID = postID
					r.Notices[i].Done = savedErr == nil
					if r.Notices[i].Done && r.Notices[i].DoneAt.IsZero() {
						r.Notices[i].DoneAt = s.now()
					}
					r.Notices[i].Error = ""
					if savedErr != nil {
						r.Notices[i].Error = Sanitize(savedErr.Error())
					}
				}
			}
			return nil
		}); e != nil {
			return e
		}
		if savedErr != nil && Sanitize(savedErr.Error()) != n.Error && first == nil {
			first = savedErr
		}
	}
	return first
}
func (s *Service) deliver(ctx context.Context, r Record, n Notice) (string, error) {
	var event room.Event = prEventOf(r, n)
	if n.Review != nil {
		event = *n.Review
	}
	if n.Merge != nil {
		merge := *n.Merge
		// The pending notice's text is the evidence the author reads today,
		// bounded when the notice was queued. The card shows the typed facts and
		// the agent text keeps this evidence.
		merge.Detail = n.Text
		event = merge
	}
	if n.Kind == "held-comment" {
		rec, err := s.roomsStore().Resolve(r.RoomID)
		if err != nil {
			return "", err
		}
		// A held comment waits for the owner in the feed; it wakes nobody. The
		// sender is the engine: the watcher is the event's producer, not its
		// author.
		post, err := s.roomsStore().PostEventInput(room.PostEventInput{RoomID: rec.ID, Event: event, ID: n.ID, To: []string{rec.Name}, NoWake: true})
		return post.ID, err
	}
	rec, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return "", err
	}
	address := rec.Name
	if existing, _ := s.Notify.Rooms.Message(r.RoomID, n.ID); existing.ID != "" {
		// The engine wakes the stored recipient. Retrying must not change
		// that recipient or post the same notice to its child author.
		return existing.ID, nil
	}
	if n.Target != "" {
		if n.System {
			info, err := s.Notify.Sessions.Store.DeliveryTarget(n.Target)
			if err != nil || !escalate.Open(info) {
				return "", fmt.Errorf("reviewer notice target is unavailable")
			}
			address = info.Address()
		} else {
			info, err := s.escalate(ctx, r, n)
			if err != nil {
				return "", err
			}
			if info.ID != "" {
				address = info.Address()
			}
		}
	}
	sender := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}
	if address == rec.Name {
		// An automatic author record is unread owner input, like a turn
		// completion. An owner-to-owner post would look like the owner's own
		// message and would disappear from owner watches and unread counts.
		if author, err := s.Notify.Sessions.Store.Resolve(r.Author); err == nil && author.RoomID == rec.ID {
			sender = notify.Sender{Kind: notify.SenderSession, Session: author}
		}
	}
	result, err := s.Notify.Post(ctx, notify.PostInput{NeedsRoot: n.NeedsRoot, NoWake: n.Kind == "informational" || address == rec.Name && !n.NeedsRoot, ID: n.ID, Event: event, Sender: sender, Mentions: []string{"@" + address}})
	return result.Message.ID, err
}

// escalate picks who receives an author notice: the first open session from
// the target up its parent chain, else the owner (zero Info). Only CI and land
// notices may start one agent for this head first. A started agent becomes the
// PR author, so the next walk starts at it.
func (s *Service) escalate(ctx context.Context, r Record, n Notice) (subagent.Info, error) {
	m := s.Notify.Sessions
	start := s.StartAgent
	if start == nil {
		start = func(o subagent.StartOptions) (subagent.Info, error) {
			o.Dir = r.Dir
			info, _, err := m.StartForEngine(o)
			return info, err
		}
	}
	req := escalate.Request{Room: r.RoomID, Subject: n.Target, Key: "pr:" + r.Key + "@" + r.Head}
	switch n.Kind {
	case "ci":
		req.Goal = fmt.Sprintf("Fix CI on %s; done when CI passes on its head.", changeLabel(r))
		req.Brief = fmt.Sprintf("Fix CI on %s (%s). Dyna made you its author.", changeLabel(r), changeLocation(r))
	case "land":
		req.Goal = fmt.Sprintf("Resolve %s; done when it is merged or closed.", changeLabel(r))
		req.Brief = fmt.Sprintf("Review the status of %s (%s). Dyna merges automatically when approved and green. Dyna made you its author.", changeLabel(r), changeLocation(r))
	}
	// Readiness goes to a child author's parent. Completion wakes the author
	// so it can finish its goal and end its turn.
	if n.Kind == "land" {
		if author, err := m.Store.Info(r.Author); err == nil && author.ParentSession != "" {
			req.Skip = []string{r.Author}
		}
	}
	info, spawned, err := escalate.Route(ctx, m, start, req)
	// Without a directory, a repository room cannot start the agent. Root can
	// repair the record, so root gets the notice instead of the owner.
	if err == nil && info.ID == "" && req.Brief != "" && r.Dir == "" {
		if root, ok, _ := m.RootSession(r.RoomID); ok && escalate.Open(root) {
			return root, nil
		}
	}
	if err == nil && spawned && r.Watching && r.State == "open" {
		if e := s.Transfer(ctx, r.Key, "author", info, ""); e != nil {
			log.Printf("PR #%d: started %s but could not make it the author: %v", r.Number, info.Address(), e)
		}
	}
	return info, err
}
