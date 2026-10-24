package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// MergeQueueItem is a view of current facts. It is never saved as queue state.
type MergeQueueItem struct {
	GoalID                   string            `json:"goalId,omitempty"`
	Expedited                bool              `json:"expedited,omitempty"`
	MissingRequiredReviewers []string          `json:"missingRequiredReviewers,omitempty"`
	Title                    string            `json:"title,omitempty"`
	Author                   string            `json:"author,omitempty"`
	RoomID                   string            `json:"roomId"`
	ChangeID                 string            `json:"changeId"`
	URL                      string            `json:"url"`
	Head                     string            `json:"head"`
	State                    string            `json:"state"`
	Reason                   string            `json:"reason,omitempty"`
	ReadyAt                  time.Time         `json:"readyAt,omitempty"`
	Main                     *MainCI           `json:"main,omitempty"`
	FailedSuite              string            `json:"failedSuite,omitempty"`
	Excerpt                  string            `json:"excerpt,omitempty"`
	Trial                    *MergeTrialStatus `json:"trial,omitempty"`
	Overlaps                 []state.Overlap   `json:"overlaps,omitempty"`
}

type MergeTrialStatus struct {
	Branch      string                  `json:"branch"`
	Head        string                  `json:"head,omitempty"`
	Phase       state.MergeTrialPhase   `json:"phase"`
	Reason      string                  `json:"reason,omitempty"`
	Batch       []string                `json:"batch"`
	MaxParallel int                     `json:"maxParallel,omitempty"`
	Stacks      []state.MergeTrialStack `json:"stacks,omitempty"`
}

func trialStatus(st State, b *state.MergeTrial) *MergeTrialStatus {
	if b == nil {
		return nil
	}
	status := &MergeTrialStatus{Branch: b.Branch, Head: b.Head, Phase: b.Phase, Reason: b.Reason, MaxParallel: b.MaxParallel}
	for _, stack := range b.Stacks {
		stack.Logs = ""
		status.Stacks = append(status.Stacks, stack)
	}
	for _, change := range b.Changes {
		if r := st.Records[change.Key]; r != nil {
			status.Batch = append(status.Batch, changeID(*r))
		} else {
			status.Batch = append(status.Batch, change.Key)
		}
	}
	return status
}

// MainWaitKind names why the queue waits on the default branch's CI. The web
// writes the words from the kind; the service never formats a sentence.
type MainWaitKind string

// Main wait kinds.
const (
	MainWaitNoPushCI MainWaitKind = "no-push-ci"
	MainWaitNoHead   MainWaitKind = "no-head"
	MainWaitPassed   MainWaitKind = "passed"
	MainWaitFailed   MainWaitKind = "failed"
	MainWaitPending  MainWaitKind = "pending"
	MainWaitPaused   MainWaitKind = "paused"
)

// MainWait is why the queue waits on the default branch, as facts. The commit
// is MainCI.Head; a screen shortens it and words the kind.
type MainWait struct {
	Kind MainWaitKind `json:"kind"`
	// Base is the default branch name, when the probe knows it.
	Base string `json:"base,omitempty"`
}

// MainCI is the saved CI result of the repository's default branch. State is
// passed, failed, pending or paused. Every item carries it, so a screen reads one field.
type MainCI struct {
	State string    `json:"state"`
	Head  string    `json:"head"`
	Wait  *MainWait `json:"wait,omitempty"`
}

// MergeQueueSnapshot derives queue facts from a saved PR snapshot without GitHub requests.
func MergeQueueSnapshot(st State) []MergeQueueItem { return mergeQueue(st) }

func mergeQueue(st State) []MergeQueueItem {
	var queue []MergeQueueItem
	for _, r := range st.Records {
		if !r.Watching || r.State != "open" {
			continue
		}
		if changeKind(*r) == "local" && !st.RepositoryFacts[defaultRepo(*r)].MergeTrialCI {
			continue
		}
		observation := facts.FromRecord(*r)
		item := MergeQueueItem{Title: r.Title, Author: r.Author, RoomID: r.RoomID, ChangeID: changeID(*r), URL: r.URL, Head: r.Head, ReadyAt: facts.ReadyAt(*r, observation)}
		item.MissingRequiredReviewers = facts.MissingRequiredReviewers(*r)
		item.Overlaps = state.SharedOverlaps(r.Overlaps)
		p, status := storedDefault(st, *r)
		if changeKind(*r) == "github" {
			item.Main = &MainCI{State: status, Head: p.Head, Wait: defaultWait(p, status)}
		}
		switch {
		case r.MergeTrialFailure != nil && r.MergeTrialFailure.Change.Head == r.Head:
			item.State, item.Reason = "trial-rejected", r.MergeTrialFailure.Reason
			if changeKind(*r) == "local" {
				item.State = "returned"
				item.FailedSuite, item.Excerpt = returnedLocalFailure(r.MergeTrialFailure.Logs)
			}
		case facts.OpenOwnerFinding(*r):
			item.State, item.Reason = "changes", "waiting on replies to owner comments"
		case len(item.MissingRequiredReviewers) > 0:
			item.State, item.Reason = "required-reviews", "waiting for required reviews: "+strings.Join(item.MissingRequiredReviewers, ", ")
		case changeKind(*r) == "local" && localMergeReady(*r):
			item.State = "ready"
		case changeKind(*r) == "local" && !facts.ApprovedAtHead(*r):
			item.State, item.Reason = "review", "needs review on the current head"
		case changeKind(*r) == "local":
			item.State, item.Reason = "waiting", "waiting for a verified local head"
		case observation.IsDraft:
			item.State, item.Reason = "held", "GitHub draft status holds this PR"
		case observation.Mergeable != nil && !*observation.Mergeable:
			item.State, item.Reason = "conflict", "conflicts with "+observation.DefaultBranch+"; rebase"
		case observation.DefaultBranch != "" && observation.Base != observation.DefaultBranch:
			item.State, item.Reason = "waiting-base", "waiting for the base PR to merge and retarget"
		case facts.Ready(*r, observation):
			item.State, item.Reason = "ready", ""
			if f := st.RepositoryFacts[defaultRepo(*r)]; f.CheckedAt.IsZero() {
				item.State, item.Reason = "waiting-repository", "waiting for repository facts"
			} else if !f.MergeTrialCI {
				item.State, item.Reason = "trial-unavailable", "requires confirmed push CI for dyna/merge-trial/**"
			}
		case !facts.HeadVerified(*r):
			item.State, item.Reason = "waiting", "waiting for a verified head"
		case r.CI == "failed":
			item.State, item.Reason = "ci-failed", ciFailedReason(*r)
		// TODO: legacy/shim remove later — a stored review from before the typed
		// results may still say question; remove when that history ages out.
		case r.RoundHead == r.Head && (r.Verdict == state.VerdictChanges || r.Verdict == "question"):
			item.State, item.Reason = "changes", map[state.ReviewVerdict]string{state.VerdictChanges: "changes requested", "question": "question for the author"}[r.Verdict]
		case r.RoundHead == r.Head && r.StopReason != "":
			item.State, item.Reason = "stopped", "the reviewer stopped early: "+string(r.StopReason)
		case !facts.ApprovedAtHead(*r):
			item.State, item.Reason = "review", "needs review"
		case r.CI != "passed":
			item.State, item.Reason = "ci", "waiting for CI"
		default:
			item.State, item.Reason = "waiting", "waiting for GitHub to confirm the PR can merge"
		}
		if b := st.MergeTrials[defaultRepo(*r)]; b != nil {
			for _, change := range b.Changes {
				if change.Key == r.Key && change.Head == r.Head {
					item.Trial = trialStatus(st, b)
					item.State, item.Reason = "trial-"+string(b.Phase), b.Reason
					break
				}
			}
		}
		if pause := st.MergePauses[r.RoomID]; pause.Reason != "" {
			item.State, item.Reason = "merge-paused", pause.Reason
		}
		queue = append(queue, item)
	}
	sort.Slice(queue, func(i, j int) bool {
		a, b := queue[i].Trial, queue[j].Trial
		if a != nil && b != nil && a.Branch == b.Branch {
			return slices.Index(a.Batch, queue[i].ChangeID) < slices.Index(b.Batch, queue[j].ChangeID)
		}
		if (a != nil) != (b != nil) {
			return a != nil
		}
		if !queue[i].ReadyAt.Equal(queue[j].ReadyAt) {
			if queue[i].ReadyAt.IsZero() {
				return false
			}
			if queue[j].ReadyAt.IsZero() {
				return true
			}
			return queue[i].ReadyAt.Before(queue[j].ReadyAt)
		}
		return queue[i].ChangeID < queue[j].ChangeID
	})
	return queue
}

// ciFailedReason names the first failed check, by name, when the record has one.
func ciFailedReason(r Record) string {
	var failed []string
	for _, c := range r.Checks {
		if c.State == "failure" {
			failed = append(failed, c.Name)
		}
	}
	if len(failed) == 0 {
		return "CI failed"
	}
	sort.Strings(failed)
	return "CI failed: " + failed[0]
}

func markPaused(item *MergeQueueItem) {
	item.State, item.Reason = "paused", "room sessions are paused"
	if item.Main != nil {
		item.Main.State, item.Main.Wait = "paused", &MainWait{Kind: MainWaitPaused}
	}
}

// RoomMergeQueue is the merge queue of one room, in the worker's order. It
// reads stored facts only, so a screen can show it without GitHub requests.
func (s *Service) RoomMergeQueue(roomID string) ([]MergeQueueItem, error) {
	st, err := s.Store.Read()
	if err != nil {
		return nil, err
	}
	ordered, err := s.prioritizedMergeQueue(context.Background(), st)
	if err != nil {
		return nil, err
	}
	rows := []MergeQueueItem{}
	for _, item := range ordered {
		if item.RoomID == roomID {
			rows = append(rows, item)
		}
	}
	return rows, nil
}

// MergeStatus reads the same facts and fixed ordering that the worker uses.
func (s *Service) MergeStatus(ctx context.Context) ([]MergeQueueItem, error) {
	st, err := s.Store.Read()
	if err != nil {
		return nil, err
	}
	pauses := map[string]bool{}
	records, err := repositoryRepresentatives(st, func(room string) (bool, error) {
		return s.roomPausedCached(room, pauses)
	})
	if err != nil {
		return nil, err
	}
	rows, err := s.prioritizedMergeQueue(ctx, st)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		p, status := storedDefault(st, r)
		// A repository row carries the same typed main facts as a PR row, so a
		// screen words them and no service sentence reaches it.
		rows = append(rows, MergeQueueItem{RoomID: r.RoomID, ChangeID: defaultRepo(r), URL: "https://" + defaultRepo(r), Head: p.Head, State: defaultQueueState(status),
			Main: &MainCI{State: status, Head: p.Head, Wait: defaultWait(p, status)}})
	}
	return rows, nil
}

// fastLandable reports a reviewer's approve-fast decision at the current head.
// The merge-ready gate has already proved required CI at that exact head.
func fastLandable(r Record) bool {
	return r.Verdict == state.VerdictApproveFast && r.CI == "passed" && r.RoundHead == r.Head
}

// fastLand publishes one change without the batch trial. Every existing merge
// gate stays: a fresh head, the approval at that head, green CI, required
// reviews, mergeability and the room pause. An approve-fast landing has no
// base lease. A tested-head landing supplies one and gets a landed event.
func (s *Service) fastLand(ctx context.Context, candidate Record, base *state.RepositoryFacts) error {
	current, children, ready, err := s.prepareMerge(ctx, candidate)
	if err != nil || !ready {
		return err
	}
	_, err = s.publishMerge(ctx, current, children, base)
	return err
}

// testedHeadLandable reports whether a fresh, ready PR can publish its own
// tested tree. mergeReady has already proved its required CI at this exact head
// through the repository's registered local or hosted gate. This check adds the
// other fact: the current default-branch tip is an ancestor of that head.
func (s *Service) testedHeadLandable(ctx context.Context, candidate Record, base state.RepositoryFacts) (bool, error) {
	if changeKind(candidate) != "github" || candidate.CI != "passed" || candidate.Head == "" || base.Head == "" || base.DefaultBranch == "" {
		return false, nil
	}
	var comparison struct {
		Status string `json:"status"`
	}
	path := fmt.Sprintf("repos/%s/compare/%s...%s", candidate.Repo, base.Head, candidate.Head)
	if err := s.github(candidate.RoomID).api(ctx, refRecord(candidate), path, &comparison); err != nil {
		return false, err
	}
	return comparison.Status == "ahead" || comparison.Status == "identical", nil
}

// prepareMerge keeps the owner repair preflight, child preparation and final fresh gate together.
func (s *Service) prepareMerge(ctx context.Context, candidate Record) (Record, []Ref, bool, error) {
	current, ready, err := s.mergeReady(ctx, candidate)
	if err != nil || !ready {
		return Record{}, nil, false, err
	}
	children, err := s.retargetMergeChildren(ctx, current)
	if err != nil {
		return Record{}, nil, false, errors.Join(err, s.rollbackMergeChildren(ctx, current, children))
	}
	latest, ready, err := s.mergeReady(ctx, current)
	if err != nil || !ready {
		return Record{}, nil, false, errors.Join(err, s.rollbackMergeChildren(ctx, current, children))
	}
	return latest, children, true, nil
}

// publishMerge owns guarded publication, outcome confirmation and reconciliation.
// A tested-head publication uses the trial publisher's atomic base lease. Other
// direct publications keep GitHub's exact-head merge guard.
// It reports whether merged state is durable even when follow-up cleanup fails.
func (s *Service) publishMerge(ctx context.Context, current Record, children []Ref, base *state.RepositoryFacts) (bool, error) {
	var mergeErr error
	if base != nil {
		batch := trialBatch(current, state.MergeTrial{Base: base.Head, DefaultBranch: base.DefaultBranch, Created: s.now()})
		mergeErr = s.trialClient(current).PublishHead(ctx, batch, current.Head)
	} else {
		_, mergeErr = s.github(current.RoomID).command(ctx, "", false, "pr", "merge", "--merge", "--match-head-commit", current.Head, "--", current.URL)
	}
	if base != nil && mergeErr == nil {
		// Publication, not GitHub's eventually consistent PR closure, owns this
		// fact. The room post ID deduplicates a later reconciliation or retry.
		ev := mergeEventOf(current, room.MergeLanded)
		ev.ReasonKind, ev.Commit = room.MergeTestedHead, current.Head
		if noticeErr := s.postMergeNotice(ctx, current, "merge-landed-"+string(room.MergeTestedHead), Notice{Merge: &ev}); noticeErr != nil {
			log.Printf("merge landed event for %s: %v", changeID(current), noticeErr)
		}
	}
	pull, readErr := s.github(current.RoomID).Pull(ctx, refRecord(current))
	if readErr == nil && pull.Merged {
		processErr := s.process(ctx, current.Key, Snapshot{Pull: pull, headConfirmed: true}, "", "", nil, nil, false)
		// Processing can defer behind another publication, or report a cleanup
		// error after saving the merge. Confirm durable state in either case.
		recorded, readErr := s.mergeRecorded(current)
		if readErr != nil {
			return false, errors.Join(processErr, readErr)
		}
		if !recorded {
			return false, errors.Join(processErr, fmt.Errorf("merged state for %s at %s is not yet durable", current.URL, current.Head))
		}
		return true, processErr
	}
	if base != nil && mergeErr == nil && readErr == nil && pull.State == "open" {
		// GitHub can take a moment to close a PR whose exact head was pushed to
		// its base. The base lease already published the tested commit, so leave
		// reconciliation to the next observation instead of starting a trial.
		return false, nil
	}
	if readErr != nil || mergeErr == nil || pull.State != "open" {
		var urls []string
		for _, child := range children {
			urls = append(urls, child.URL())
		}
		text := fmt.Sprintf("The merge outcome of %s at %s is unknown. Child PRs remain on %s: %s. If the parent stays open and is no longer ready, restore these child bases to %s by hand.", current.URL, current.Head, current.DefaultBranch, strings.Join(urls, ", "), current.Branch)
		ev := mergeEventOf(current, room.MergeFailed)
		ev.Base, ev.Reason, ev.ReasonKind = current.DefaultBranch, "the merge outcome is unknown", room.MergeReasonOther
		noticeErr := s.postMergeNotice(ctx, current, "merge-unknown", Notice{Merge: &ev, Text: text})
		unknownErr := fmt.Errorf("GitHub has not confirmed the merge of %s", current.URL)
		return false, errors.Join(unknownErr, mergeErr, readErr, noticeErr)
	}
	rollbackErr := s.rollbackMergeChildren(ctx, current, children)
	_, refreshErr := s.refreshMergeFacts(ctx)
	return false, errors.Join(mergeErr, rollbackErr, refreshErr)
}

// mergeRecorded is the common durable-outcome check for repair and trial publication.
func (s *Service) mergeRecorded(expected Record) (bool, error) {
	saved, err := s.Store.Get(expected.Key)
	if err != nil {
		return false, err
	}
	return saved.State == "merged" && !saved.Watching && saved.Head == expected.Head, nil
}

// mergeReady is the same complete gate before and after preparation. It
// refreshes remote metadata through the shared writer, then current CI.
func (s *Service) mergeReady(ctx context.Context, expected Record) (Record, bool, error) {
	st, err := s.Store.readRecords(expected.Key)
	if err != nil {
		return Record{}, false, err
	}
	current := st.Records[expected.Key]
	if current == nil || current.Head != expected.Head || !current.Watching || current.State != "open" {
		return Record{}, false, nil
	}
	if paused, err := s.RoomPaused(current.RoomID); err != nil {
		return Record{}, false, err
	} else if paused {
		return Record{}, false, nil
	}
	if changeKind(*current) == "github" {
		local, err := recordLocalLane(ctx, *current)
		if err != nil || local {
			return Record{}, false, err
		}
	}
	if changeKind(*current) == "local" {
		enabled, err := locallane.Enabled(ctx, current.RepoPath)
		if err != nil || !enabled {
			return Record{}, false, err
		}
		if err := s.refreshLocal(ctx, current.Key); err != nil {
			return Record{}, false, err
		}
		saved, err := s.Store.Get(current.Key)
		return saved, err == nil && saved.Head == expected.Head && localMergeReady(saved), err
	}
	err = s.ConfirmHeads(ctx, current.Key)
	if err != nil {
		return Record{}, false, err
	}
	st, err = s.Store.readRecords(expected.Key)
	if err != nil {
		return Record{}, false, err
	}
	current = st.Records[expected.Key]
	if current == nil || current.Head != expected.Head || !current.Watching || current.State != "open" {
		return Record{}, false, nil
	}
	// The queue re-reads a ready PR every poll. Revalidate its saved copies so
	// an unchanged PR costs 304 replies, not counted requests, and the read
	// waits for a quota pause like every other poll.
	gh, clientErr := s.mergePollClient(st, *current)
	if clientErr != nil {
		return Record{}, false, clientErr
	}
	snap, err := gh.Snapshot(ctx, refRecord(*current))
	if saveErr := s.savePollResponses(current.Key, gh); saveErr != nil {
		return Record{}, false, errors.Join(err, saveErr)
	}
	if err != nil {
		return Record{}, false, err
	}
	st, err = s.Store.readRecords(expected.Key)
	if err != nil {
		return Record{}, false, err
	}
	current = st.Records[expected.Key]
	if current == nil || current.Head != expected.Head || snap.Pull.Head.SHA != current.Head || !current.Watching {
		return Record{}, false, nil
	}
	policy := EffectivePolicy(st, *current)
	snap, err = s.withRegisteredCI(ctx, *current, snap, policy)
	if err != nil {
		return Record{}, false, err
	}
	ci, key, jobs, _ := s.ciStatus(snap, *current, policy)
	if err := s.process(ctx, current.Key, snap, ci, key, jobs, nil, false); err != nil {
		return Record{}, false, err
	}
	st, err = s.Store.readRecords(expected.Key)
	if err != nil {
		return Record{}, false, err
	}
	current = st.Records[expected.Key]
	if current == nil || current.Head != expected.Head {
		return Record{}, false, nil
	}
	policy = EffectivePolicy(st, *current)
	if !facts.Ready(*current, snap.GitHubFacts(*current, policy.RequiredChecks...)) {
		return Record{}, false, nil
	}
	if paused, err := s.RoomPaused(current.RoomID); err != nil {
		return Record{}, false, err
	} else if paused {
		return Record{}, false, nil
	}
	return *current, true, nil
}

// The existing batched-head reader owns observed PR facts. A second pass after
// a merge reports newly DIRTY PRs before this reconcile pass returns.
func (s *Service) refreshMergeFacts(ctx context.Context) (map[string]headProbe, error) {
	localErr := s.refreshLocalMergeFacts(ctx)
	st, err := s.Store.Read()
	if err != nil {
		return nil, errors.Join(localErr, err)
	}
	refreshCtx, stop := context.WithTimeout(ctx, s.recordBudget())
	defer stop()
	probes, refreshErr := s.refreshHeads(refreshCtx, st, s.now(), nil)
	st, err = s.Store.Read()
	if err != nil {
		return probes, errors.Join(localErr, refreshErr, err)
	}
	keys := make([]string, 0, len(st.Records))
	for key := range st.Records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	failures := []error{localErr, refreshErr}
	for _, key := range keys {
		r := st.Records[key]
		if !r.Watching || r.State != "open" || changeKind(*r) != "github" || !facts.HeadVerified(*r) {
			continue
		}
		if paused, err := s.RoomPaused(r.RoomID); err != nil {
			failures = append(failures, err)
			continue
		} else if paused {
			continue
		}
		observation := facts.FromRecord(*r)
		if observation.Mergeable != nil && !*observation.Mergeable {
			if err := s.postMergeConflict(ctx, *r, conflictBase(*r)); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return probes, errors.Join(failures...)
}

// conflictBase is the branch the PR must rebase onto. A stacked PR has a
// parent branch as its actual base; the repository default is only a fallback.
func conflictBase(r Record) string {
	if r.BaseRef != "" {
		return r.BaseRef
	}
	return r.DefaultBranch
}

// The room log is the dedupe record. No conflict flag or attempt state is saved.
func (s *Service) postMergeConflict(ctx context.Context, r Record, branch string) error {
	if branch == "" || r.IsDraft {
		return nil
	}
	ev := mergeEventOf(r, room.MergeConflict)
	ev.Base, ev.Reason, ev.ReasonKind = branch, "conflicts with "+branch, room.MergeConflictReason
	return s.postMergeNotice(ctx, r, "merge-conflict", Notice{Target: r.Author, Merge: &ev, Text: fmt.Sprintf("%s conflicts with %s at %s; rebase onto %s.", changeLabel(r), branch, r.Head, branch)})
}

// mergeNoticeID gives the merge worker one notice identity per head.
func mergeNoticeID(r Record, token string) string {
	// TODO: legacy/shim remove later — reuse retained conflict IDs that included the old base SHA until those PR heads change.
	if token == "merge-conflict" {
		for _, n := range r.Notices {
			if n.Target == r.Author && strings.Contains(n.Text, "\nHead: "+r.Head+"\n") && strings.Contains(n.Text, " needs a rebase before merging onto ") {
				return n.ID
			}
		}
	}
	return "msg_" + digest(token + "/" + r.Key + "/" + r.Head)[:16]
}

func (s *Service) postMergeNotice(ctx context.Context, r Record, token string, n Notice) error {
	n.ID = mergeNoticeID(r, token)
	if s.Notify == nil || s.Notify.Rooms == nil {
		return fmt.Errorf("merge notification needs the room store")
	}
	if _, err := s.Notify.Rooms.Message(r.RoomID, n.ID); err == nil {
		return nil
	} else if !errors.Is(err, room.ErrPostNotFound) {
		return err
	}
	deliver := s.Deliver
	if deliver == nil {
		deliver = s.deliver
	}
	_, err := deliver(ctx, r, n)
	return err
}

func (s *Service) rollbackMergeChildren(ctx context.Context, r Record, children []Ref) error {
	var failures []error
	var failed []string
	for _, child := range children {
		if _, err := s.github(r.RoomID).command(ctx, "", false, "pr", "edit", child.URL(), "--base", r.Branch); err != nil {
			failures = append(failures, fmt.Errorf("restore %s to %s: %w", child.URL(), r.Branch, err))
			failed = append(failed, child.URL())
		}
	}
	if len(failed) == 0 {
		return nil
	}
	// The room owner receives one aggregate post for this parent head. The
	// room log also makes the failed child URLs visible to their authors.
	text := fmt.Sprintf("Dyna could not restore child PR bases after a failed merge of %s at %s. Restore these PRs to %s before merging them: %s", changeLabel(r), r.Head, r.Branch, strings.Join(failed, ", "))
	ev := mergeEventOf(r, room.MergeRolledBack)
	ev.Base, ev.Reason, ev.ReasonKind = r.Branch, "child PR bases could not be restored", room.MergeReasonOther
	failures = append(failures, s.postMergeNotice(ctx, r, "merge-rollback", Notice{Merge: &ev, Text: text}))
	return errors.Join(failures...)
}

// Finish child recovery before ending the watch, including externally merged PRs.
// A failed edit leaves the watch active so the next observation retries it.
func (s *Service) retargetObservedMerge(ctx context.Context, key string, pull Pull) error {
	if pull.State != "closed" || !pull.Merged {
		return nil
	}
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	current := st.Records[key]
	if current == nil {
		return nil
	}
	r := *current
	// Head refresh marks merged PRs before the snapshot ends their watch.
	if !r.Watching || changeKind(r) != "github" || r.Head != pull.Head.SHA {
		return nil
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return nil
	}
	rm, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return err
	}
	if ghclient.Access(ctx, rm) != ghclient.ReadWrite {
		return nil
	}
	local, err := recordLocalLane(ctx, r)
	if err != nil || local {
		return err
	}
	if paused, err := s.RoomPaused(r.RoomID); err != nil {
		return err
	} else if paused {
		return errors.New("room sessions are paused; child retargeting must wait")
	}
	r.Branch, r.DefaultBranch = pull.Head.Ref, pull.Base.Repo.DefaultBranch
	_, err = s.retargetMergeChildren(ctx, r)
	return err
}

func (s *Service) retargetMergeChildren(ctx context.Context, r Record) ([]Ref, error) {
	observation := facts.FromRecord(r)
	if r.Branch == "" || observation.DefaultBranch == "" {
		return nil, fmt.Errorf("GitHub did not return the PR branch and default branch")
	}
	pulls, err := s.github(r.RoomID).openPulls(ctx, refRecord(r))
	if err != nil {
		return nil, err
	}
	var children []Ref
	sort.Slice(pulls, func(i, j int) bool { return pulls[i].Number < pulls[j].Number })
	for _, child := range pulls {
		if child.Number == r.Number || child.State != "open" || child.Base.Ref != r.Branch {
			continue
		}
		ref := refRecord(r)
		ref.Number = child.Number
		children = append(children, ref)
		if _, err := s.github(r.RoomID).command(ctx, "", false, "pr", "edit", ref.URL(), "--base", observation.DefaultBranch); err != nil {
			return children, fmt.Errorf("retarget child PR #%d before merging PR #%d: %w", child.Number, r.Number, err)
		}
	}
	return children, nil
}
