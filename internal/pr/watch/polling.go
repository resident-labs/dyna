package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/ghquota"
	"dyna-agent/internal/pr/state"
)

// Polling is independent of the CI wake pause and review assignment state.
type Polling = state.Polling

func observe(p *Polling, now time.Time, snap Snapshot, reserve bool) {
	b, _ := json.Marshal(snap)
	fingerprint := digest(string(b))
	if fingerprint != p.Fingerprint || p.LastChange.IsZero() {
		p.Fingerprint, p.LastChange = fingerprint, now
	}
	interval := 1
	if reserve {
		interval = 5
	}
	p.IntervalMinutes = interval
	p.Next = nextPollTime(now, interval)
}

// refreshUnwatched reads only Pull metadata. Stopped watches do not fetch CI or
// comments, dispatch reviewers, send notices, or run merged worktree cleanup.
// force is used once when a watch stops; subsequent attempts run at most hourly.
func (s *Service) refreshUnwatched(ctx context.Context, key string, force bool, pauses map[string]bool) error {
	return s.refreshUnwatchedFromProbe(ctx, key, force, pauses, nil)
}

func (s *Service) refreshUnwatchedFromProbe(ctx context.Context, key string, force bool, pauses map[string]bool, probe *headProbe) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	r := st.Records[key]
	now := s.now()
	if !shouldRefreshUnwatched(st, r, now, force) {
		return nil
	}
	paused, err := s.roomPausedCached(r.RoomID, pauses)
	if err != nil || paused {
		return err
	}
	gh := s.github(r.RoomID)
	gh.requests = &requestCache{entries: st.Responses[key], used: map[string]cachedResponse{}, rates: st.RateLimits, legacyPause: st.LegacyPause, now: s.now}
	var pull Pull
	var requestErr error
	if probe != nil {
		pull = probe.Pull
		gh.requests.account = probe.Account
	} else {
		pull, requestErr = gh.Pull(ctx, refRecord(*r))
	}
	if requestErr == nil && pull.State != "open" && pull.State != "closed" {
		requestErr = fmt.Errorf("GitHub returned an invalid pull request state")
	}
	saveErr := s.Store.update(func(current *State) error {
		applyRateLimits(current, st.RateLimits, s.now())
		live := current.Records[key]
		// A resumed or completed watch owns its state and response cache again.
		if live == nil || live.Watching || live.State != state.PROpen || changeKind(*live) == "local" {
			return nil
		}
		if current.Responses == nil {
			current.Responses = map[string]map[string]cachedResponse{}
		}
		if requestErr == nil && probe == nil {
			current.Responses[key] = gh.requests.used
		} else if requestErr != nil {
			if current.Responses[key] == nil {
				current.Responses[key] = map[string]cachedResponse{}
			}
			for path, response := range gh.requests.used {
				current.Responses[key][path] = response
			}
		}
		if gh.requests.account != "" {
			live.QuotaAccount = gh.requests.account
			current.RefreshQuotaDisplay()
		}
		live.Checked = s.now()
		live.Polling.IntervalMinutes = 60
		live.Polling.Next = live.Checked.Add(time.Hour)
		if quotaFor(st, live, s.now()).PauseUntil.After(live.Polling.Next) {
			live.Polling.Next = quotaFor(st, live, s.now()).PauseUntil
		}
		if requestErr != nil {
			live.Error = Sanitize(requestErr.Error())
			return nil
		}
		live.Error = ""
		if pull.State == "closed" {
			endRecord(live, pull.Merged, s.now())
		}
		return nil
	})
	if saveErr != nil {
		return saveErr
	}
	return requestErr
}

// shouldRefreshUnwatched also lets Tick skip historical records without another
// full state read for each one. Refresh still rechecks under a fresh snapshot.
func shouldRefreshUnwatched(st State, r *Record, now time.Time, force bool) bool {
	return r != nil && !r.Watching && r.State == state.PROpen && changeKind(*r) != "local" &&
		!now.Before(quotaFor(st, r, now).PauseUntil) && (force || !now.Before(r.Polling.Next))
}

// refreshUnwatchedBatch runs after watched PR work, so stopped watches cannot
// delay head confirmation or review dispatch. Failed probes fall back to the
// existing REST reader.
func (s *Service) refreshUnwatchedBatch(ctx context.Context, initial State, keys []string, pauses map[string]bool) error {
	due := false
	for _, key := range keys {
		if shouldRefreshUnwatched(initial, initial.Records[key], s.now(), false) {
			due = true
			break
		}
	}
	if !due {
		return nil
	}
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	var records []*Record
	var failures []error
	for _, key := range keys {
		r := st.Records[key]
		if !shouldRefreshUnwatched(st, r, s.now(), false) {
			continue
		}
		paused, err := s.roomPausedCached(r.RoomID, pauses)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !paused {
			records = append(records, r)
		}
	}
	if len(records) == 0 {
		return errors.Join(failures...)
	}
	gh := s.github("")
	gh.unwatchedHeads = true
	gh.requests = &requestCache{rates: st.RateLimits, legacyPause: st.LegacyPause, now: s.now}
	batchCtx, stop := context.WithTimeout(ctx, s.recordBudget())
	heads, rates, _ := gh.readHeads(batchCtx, records)
	stop()
	if err := s.Store.update(func(current *State) error {
		applyRateLimits(current, rates, s.now())
		return nil
	}); err != nil {
		return err
	}
	for _, r := range records {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		var probe *headProbe
		if head, ok := heads[r.Key]; ok && head.Err == nil {
			probe = &head
		}
		recordCtx, cancel := context.WithTimeout(ctx, s.recordBudget())
		err := s.refreshUnwatchedFromProbe(recordCtx, r.Key, false, pauses, probe)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// pullVersion ignores fields that GitHub can recompute without updating the PR.
func pullVersion(p Pull, policy ChangePolicy) string {
	b, _ := json.Marshal(struct {
		Updated, Head, State string
		Draft, Merged        bool
		Labels               []string
		Policy               ChangePolicy
	}{p.Updated, p.Head.SHA, p.State, p.Draft, p.Merged, p.labelNames(), policy})
	return digest(string(b))
}

// pollSnapshot avoids CI, discussion and artifact requests on a quiet PR.
// The version is saved only by process after a complete poll, so partial or
// failed snapshots cannot turn the next attempt into a cheap success.
func (g GitHub) pollSnapshot(ctx context.Context, r Record, policy ChangePolicy) (Snapshot, bool, error) {
	ref := refRecord(r)
	pull, err := g.Pull(ctx, ref)
	if err != nil {
		return Snapshot{}, false, err
	}
	return g.pollSnapshotFromPull(ctx, r, policy, pull, "")
}

func (g GitHub) pollSnapshotFromPull(ctx context.Context, r Record, policy ChangePolicy, pull Pull, ciProbe string) (Snapshot, bool, error) {
	g.runPhase = ghquota.RunWatchSnapshot
	ref := refRecord(r)
	now := time.Now()
	if g.requests != nil {
		now = g.requests.clock()
	}
	notModified := g.requests != nil && g.requests.notModified[strings.ToLower(ref.Host)+"/"+ref.path("")]
	if (g.requests == nil || !g.requests.forceSnapshot) && quietPull(r, policy, pull, ciProbe, notModified, now) {
		return Snapshot{Pull: pull, ciProbe: ciProbe}, true, nil
	}
	savedCI := g.settledPollCI(ctx, r, pull, ciProbe, now)
	// Metadata is not a documented signal for every discussion edit. Re-read
	// all pages at least every nine minutes, even while CI remains pending.
	if g.requests != nil && !g.requests.forceSnapshot && !g.requests.forceBudgets &&
		pull.State == "open" && pull.Updated != "" && pull.DiscussionVersion != "" && r.Polling.Error == "" &&
		r.Polling.PullVersion == pullVersion(pull, policy) && r.Polling.DiscussionVersion == pull.DiscussionVersion && !r.Polling.DiscussionChecked.IsZero() &&
		now.Before(r.Polling.DiscussionChecked.Add(9*time.Minute)) {
		saved := g
		saved.requests = &requestCache{cacheOnly: true, entries: g.requests.entries}
		if comments, err := saved.Comments(ctx, ref); err == nil {
			for key, response := range saved.requests.used {
				g.requests.use(key, response)
			}
			g.requests.savedComments = &comments
		}
	}
	snap, err := g.snapshotFromPull(ctx, ref, pull, savedCI)
	snap.ciProbe = ciProbe
	return snap, false, err
}

// settledPollCI shares the normal CI reader with saved response inspection.
// Only a complete observation for an unchanged, settled head may be reused.
// Missing pages or invalid data fall back to the normal network read.
func (g GitHub) settledPollCI(ctx context.Context, r Record, pull Pull, ciProbe string, now time.Time) *Snapshot {
	if g.requests == nil || g.requests.forceSnapshot || g.requests.forceBudgets || pull.State != "open" ||
		ciProbe == "" || r.ChecksAt.IsZero() || ciProbe != r.Polling.CIVersion || !settledCI(r) || !headVerified(r) ||
		r.Head != pull.Head.SHA || r.Polling.PullHead != r.Head || r.Polling.PullVersion == "" ||
		r.Polling.Error != "" || pendingTestReports(r) || fullPollDue(r, now) {
		return nil
	}
	saved := g
	saved.requests = &requestCache{cacheOnly: true, countSavedCI: true, entries: g.requests.entries}
	var ci Snapshot
	if err := saved.readCommitCI(ctx, refRecord(r), r.Head, &ci); err != nil {
		return nil
	}
	for key, response := range saved.requests.used {
		g.requests.use(key, response)
	}
	return &ci
}

// quietPull reports that pull and the CI probe show nothing new since the
// last complete poll, so the PR needs no CI, discussion or artifact requests.
func quietPull(r Record, policy ChangePolicy, pull Pull, ciProbe string, notModified bool, now time.Time) bool {
	return pull.State == "open" && pull.DiscussionVersion != "" && r.Polling.DiscussionVersion == pull.DiscussionVersion && settledCI(r) && headVerified(r) && r.Polling.Error == "" && (ciProbe == "" || ciProbe == r.Polling.CIVersion) &&
		r.Head == pull.Head.SHA && r.Polling.PullVersion == pullVersion(pull, policy) &&
		// A retarget that waits on its parent's merge needs another full poll.
		(pull.Base.Ref == "" || pull.Base.Ref == r.BaseRef) &&
		(pull.Updated != "" || notModified) && !pendingTestReports(r) && !fullPollDue(r, now)
}

// Keep polls full while CI is pending, so the latest attempt replaces the old
// result. Failed or cancelled CI is settled once every check ended: a rerun
// or a new head changes the head batch's CI probe, which polls the PR again.
// A budget failure can mask a check that is still running, so it stays
// unsettled.
func settledCI(r Record) bool {
	if r.CI == "" || r.CI == state.CIPending {
		return false
	}
	for _, check := range r.Checks {
		if check.State == "pending" {
			return false
		}
	}
	return true
}

// Spread safety polls over fixed slots. Active PRs keep their ten-minute
// bound. Settled drafts wait 50 to 60 minutes; the minute head batch still
// detects ready, head, CI and metadata changes. Unfinished work stays active.
func fullPollDue(r Record, now time.Time) bool {
	last := r.Polling.FullChecked
	if last.IsZero() {
		return true
	}
	cycle := 9 * time.Minute
	if r.IsDraft && settledCI(r) {
		last = last.Add(50 * time.Minute)
		cycle = 10 * time.Minute
	}
	hash, _ := strconv.ParseUint(digest(r.Key)[:8], 16, 32)
	next := last.Truncate(cycle).Add(time.Duration(hash%uint64(cycle/time.Minute)) * time.Minute)
	if !next.After(last) {
		next = next.Add(cycle)
	}
	return !now.Before(next)
}

// Watch uses a fixed minute ticker. Scheduling from completion plus one minute
// would miss the next tick whenever a request took time to finish.
func nextPollTime(now time.Time, interval int) time.Time {
	if interval > 1 {
		return now.Add(time.Duration(interval) * time.Minute)
	}
	return now.Truncate(time.Minute).Add(time.Minute)
}
