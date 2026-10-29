package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"sync"
	"time"

	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/state"
)

const (
	snapshotWorkers = 4
	noticesPerTick  = 4
)

// withTickTimeout lets tests expire the polling phase without waiting.
var withTickTimeout = context.WithTimeout

func (s *Service) recordBudget() time.Duration {
	if s.recordTimeout > 0 {
		return s.recordTimeout
	}
	return 15 * time.Second
}

// tickKeys puts first polls and changed heads first, then polls oldest first.
// Pending CI must not let recently checked PRs starve older quiet PRs.
func tickKeys(st State) []string {
	keys := make([]string, 0, len(st.Records))
	for key := range st.Records {
		keys = append(keys, key)
	}
	priority := func(r *Record) int {
		if r == nil {
			return 4
		}
		if !r.Watching || r.State != state.PROpen {
			return 3
		}
		if r.Checked.IsZero() || r.HeadSeen.After(r.Checked) {
			return 0
		}
		return 1
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := st.Records[keys[i]], st.Records[keys[j]]
		if x, y := priority(a), priority(b); x != y {
			return x < y
		}
		if a != nil && b != nil && !a.Checked.Equal(b.Checked) {
			return a.Checked.Before(b.Checked)
		}
		return keys[i] < keys[j]
	})
	return keys
}

// TODO: legacy/shim remove later — ignore old quiet Next times after upgrade.
func pollDue(st State, r *Record, now time.Time) bool {
	return r != nil && r.Watching && changeKind(*r) != "local" &&
		!now.Before(quotaFor(st, r, now).PauseUntil) && (!now.Before(r.Polling.Next) ||
		(r.Polling.IntervalMinutes > 1 && !reserve(quotaFor(st, r, now), now) && !now.Before(r.Checked.Add(time.Minute)))) &&
		!(reserve(quotaFor(st, r, now), now) && !r.Checked.IsZero() && now.Before(r.Checked.Add(5*time.Minute)))
}

type pollResult struct {
	key       string
	gh        GitHub
	snap      Snapshot
	err       error
	started   time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	unchanged bool
	polledAt  time.Time
}

// pollEvery is how often the PR passes look again while they have work.
// GitHub and CI send the engine no events, so the head batch, merge trials
// and sync operations are polled.
const pollEvery = time.Minute

// Tick polls the watched PRs, delivers their notices, and returns when it is
// next due: one poll later while a PR is watched or a notice is still
// pending, and not before a legacy pause ends; otherwise zero. A new watch
// wakes it by its store event. A pass that stops early is due a poll later.
func (s *Service) Tick(ctx context.Context) (time.Time, error) {
	retry := func(err error) (time.Time, error) { return s.now().Add(pollEvery), err }
	if err := ctx.Err(); err != nil {
		return retry(err)
	}
	budget := s.tickTimeout
	if budget <= 0 {
		budget = 50 * time.Second
	}
	ctx = context.WithValue(ctx, tickReadKey{}, tickReadScope{s, s.tickView()})
	ctx = locallane.WithCommonDirs(ctx)
	budgetParent := ctx
	ctx, cancel := withTickTimeout(ctx, budget)
	defer cancel()
	// Reserve time for dispatch even when head refreshes use their whole phase.
	pollCtx, stopPoll := context.WithTimeout(ctx, budget-min(s.recordBudget(), budget/2))
	defer stopPoll()
	polledAt := s.now()
	pauses := map[string]bool{}
	st, err := s.readTickState(ctx)
	if err != nil {
		return retry(err)
	}
	headCtx, stopHeads := context.WithTimeout(ctx, s.recordBudget())
	owners := s.loadWorktreeOwners(headCtx, st)
	discovery := s.discoveryRecords(headCtx, st, owners)
	heads, headErr := s.refreshHeads(headCtx, st, polledAt, pauses, discovery...)
	stopHeads()
	st, err = s.readTickState(ctx)
	if err != nil {
		return retry(errors.Join(headErr, err))
	}
	if e := s.scheduleRegisteredCI(ctx, st); e != nil {
		headErr = errors.Join(headErr, e)
	}
	s.scheduleCacheWarm(ctx, st)
	// Local branch reads also precede reconciliation, so a moved local head cannot retain a reviewer.
	for _, key := range tickKeys(st) {
		r := st.Records[key]
		if r == nil || !r.Watching || changeKind(*r) != "local" {
			continue
		}
		recordCtx, stop := context.WithTimeout(ctx, s.recordBudget())
		headErr = errors.Join(headErr, s.refreshLocal(recordCtx, key))
		stop()
	}
	// Replay uses at most half the remaining poll time. The shared poll
	// deadline still reserves dispatch time, and normal polling keeps a share.
	pollDeadline, _ := pollCtx.Deadline()
	replayCtx, stopReplay := context.WithTimeout(pollCtx, min(s.recordBudget(), time.Until(pollDeadline)/2))
	if e := s.replayPendingVerdicts(replayCtx, heads); e != nil {
		headErr = errors.Join(headErr, e)
	}
	stopReplay()
	if e := s.releaseDeferredReviewers(ctx); e != nil {
		headErr = errors.Join(headErr, e)
	}
	// Reconcile before ancillary requests and their full-state writes.
	if e := s.dispatch(ctx, false); e != nil {
		headErr = errors.Join(headErr, e)
	}
	st, err = s.readTickState(ctx)
	if err != nil {
		return retry(errors.Join(headErr, err))
	}
	keys := tickKeys(st)
	// Workers never write state. Each cache belongs to one record, and all
	// requests share a locked rate limit. Store updates remain serialized below.
	rates := st.RateLimits
	var rateMu sync.Mutex
	recoverBudgets := map[string]bool{}
	jobs := make(chan string, len(keys))
	results := make(chan pollResult, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return retry(err)
		}
		// A quiet PR needs no job unless a committed budget still needs
		// reconciliation. Its saved CI evidence selects the normal full poll.
		if probe, ok := heads[key]; ok && probe.Err == nil && pollDue(st, st.Records[key], s.now()) {
			r := *st.Records[key]
			policy := EffectivePolicy(st, r)
			if quietPull(r, policy, probe.Pull, probe.ciVersion(), false, s.now()) {
				if !s.quietBudgetRecovery(ctx, r, policy, probe.Pull, st.Responses[key]) {
					continue
				}
				recoverBudgets[key] = true
			}
			paused, err := s.roomPausedCached(st.Records[key].RoomID, pauses)
			if err != nil {
				return retry(err)
			}
			if !paused {
				jobs <- key
			}
		}
	}
	close(jobs)
	var workers sync.WaitGroup
	workerCount := min(snapshotWorkers, len(jobs))
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for key := range jobs {
				if pollCtx.Err() != nil {
					return
				}
				now := s.now()
				r := st.Records[key]
				rateMu.Lock()
				rate := quotaFor(st, r, now)
				paused := now.Before(rate.PauseUntil) || (reserve(rate, now) && !r.Checked.IsZero() && now.Before(r.Checked.Add(5*time.Minute)))
				rateMu.Unlock()
				if paused {
					continue
				}
				started := time.Now()
				recordCtx, stop := context.WithTimeout(pollCtx, s.recordBudget())
				gh := s.github(r.RoomID)
				gh.requests = &requestCache{forceBudgets: fullPollDue(*r, now), forceSnapshot: recoverBudgets[key], entries: st.Responses[key], used: map[string]cachedResponse{}, rates: rates, legacyPause: st.LegacyPause, rateMu: &rateMu, now: s.now}
				snap, unchanged, e := gh.pollSnapshotFromPull(recordCtx, *r, EffectivePolicy(st, *r), heads[key].Pull, heads[key].ciVersion())
				snap.headConfirmed = true
				slog.Debug("PR watcher phase", "key", key, "phase", "snapshot", "duration", time.Since(started), "error", e)
				results <- pollResult{key: key, gh: gh, snap: snap, err: e, started: started, ctx: recordCtx, cancel: stop, unchanged: unchanged, polledAt: polledAt}
			}
		}()
	}
	go func() { workers.Wait(); close(results) }()
	var first error = headErr
	remember := func(e error) {
		if e != nil && first == nil {
			first = e
		}
	}
	// Consume completed snapshots in completion order. A stalled request does
	// not hold up another record's snapshot or state processing.
	var budgets []pollResult
	for result := range results {
		// Save attempted failures even when the whole tick has expired.
		started := time.Now()
		remember(s.finishPoll(result.ctx, st, result))
		result.cancel()
		if result.err == nil && !result.unchanged {
			budgets = append(budgets, result)
		}
		slog.Debug("PR watcher phase", "key", result.key, "phase", "process", "duration", time.Since(started))
		slog.Debug("PR watcher record", "key", result.key, "duration", time.Since(result.started))
	}
	if ctx.Err() != nil {
		return retry(errors.Join(first, ctx.Err()))
	}

	// Budget checks read job lists and download artifacts, which can be slow.
	// They run after the head refresh and the review dispatch, so they never
	// delay those. Notices they add go out with this tick's delivery. A check that misses this tick runs again on a
	// later full poll, or at once when a test report is still pending.
	for _, result := range budgets {
		if ctx.Err() != nil {
			break
		}
		started := time.Now()
		remember(s.runBudgets(budgetParent, result, pauses))
		slog.Debug("PR watcher phase", "key", result.key, "phase", "budgets", "duration", time.Since(started))
	}
	// Long artifact reads may exhaust polling time. Give delivery its own
	// bounded phase, while preserving cancellation from the service caller.
	ctx, stopDelivery := context.WithTimeout(budgetParent, budget)
	defer stopDelivery()
	// The radar runs local git only, after GitHub work, so its notices go out
	// with this tick's delivery. Its failures never fail the poll.
	radarCtx, stopRadar := context.WithTimeout(ctx, s.recordBudget())
	if e := s.refreshOverlaps(radarCtx); e != nil {
		slog.Warn("PR conflict radar failed", "error", e)
	}
	stopRadar()
	fresh, e := s.readTickState(ctx)
	if e != nil {
		return retry(e)
	}
	// The pass is due again while a PR is watched or a notice was pending: a
	// delivery can fail, and flush sends only a few notices per pass.
	var next time.Time
	for _, key := range tickKeys(fresh) {
		if ctx.Err() != nil {
			return retry(errors.Join(first, ctx.Err()))
		}
		r := fresh.Records[key]
		if r != nil && r.Watching {
			next = s.now().Add(pollEvery)
		}
		if !hasPendingNotices(r) {
			continue
		}
		next = s.now().Add(pollEvery)
		started := time.Now()
		recordCtx, stop := context.WithTimeout(ctx, s.recordBudget())
		remember(s.flush(recordCtx, key))
		stop()
		slog.Debug("PR watcher phase", "key", key, "phase", "deliver", "duration", time.Since(started))
	}
	if !next.IsZero() && fresh.LegacyPause.After(next) {
		next = fresh.LegacyPause
	}
	for _, key := range keys {
		if ctx.Err() != nil {
			return retry(errors.Join(first, ctx.Err()))
		}
		if !shouldRefreshUnwatched(st, st.Records[key], s.now(), false) {
			continue
		}
		recordCtx, stop := context.WithTimeout(ctx, s.recordBudget())
		started := time.Now()
		remember(s.refreshUnwatched(recordCtx, key, false, pauses))
		stop()
		slog.Debug("PR watcher phase", "key", key, "phase", "unwatched", "duration", time.Since(started))
	}
	issueCtx, stopIssues := context.WithTimeout(ctx, s.recordBudget())
	issuesWatched, issueErr := s.pollIssueWatches(issueCtx)
	remember(issueErr)
	stopIssues()
	// A watched issue keeps the tick due even when no PR is watched.
	if issuesWatched && next.IsZero() {
		next = s.now().Add(pollEvery)
	}
	pollErr := errors.Join(first, ctx.Err(), s.cleanupPublicationGates())
	// Discovery uses this poll's existing repository observation. Tracking a
	// new PR is ancillary and cannot turn a completed poll into a failure.
	s.autoTrack(ctx, fresh, heads, owners)
	if pollErr != nil {
		return retry(pollErr)
	}
	return next, nil
}

// readBudgetSnapshot is the committed reader; tests control a write after its read.
var readBudgetSnapshot = state.ReadSnapshot

// runBudgets enforces the CI time and test budgets for a refreshed head with
// its own time limit, then keeps the cached responses of those requests.
func (s *Service) runBudgets(ctx context.Context, result pollResult, pauses map[string]bool) (err error) {
	// Allow the artifact reader its full 90-second limit, plus other budget reads.
	// The caller retains shutdown cancellation, without the short polling deadline.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var r Record
	var policy ChangePolicy
	requestsSaved := false
	defer func() {
		if requestsSaved || result.gh.requests == nil {
			return
		}
		// A moved target cannot discard observed account-wide rate evidence.
		fresh, saveErr := state.ReadSnapshot(s.Store.Root)
		if saveErr == nil {
			saveErr = s.saveBudgetRequests(result, r, policy, fresh)
		}
		err = errors.Join(err, saveErr)
	}()
	observed, err := readBudgetSnapshot(s.Store.Root)
	if err != nil {
		return err
	}
	r, ok := observed.Record(result.key)
	if !ok || !r.Watching || r.Head != result.snap.Pull.Head.SHA {
		return nil
	}
	paused, err := s.roomPausedCached(r.RoomID, pauses)
	if err != nil || paused {
		return err
	}
	policy = EffectivePolicy(observed.Metadata(), r)
	result.snap, err = s.hostedWithoutLocalCancellations(ctx, r, result.snap)
	if err != nil {
		return err
	}
	err = s.enforceCIBudget(ctx, result.gh, r, result.snap, policy)
	if err == nil {
		beforeReports, readErr := readBudgetSnapshot(s.Store.Root)
		if readErr != nil {
			return errors.Join(err, readErr)
		}
		live, ok := beforeReports.Record(result.key)
		if !ok || !budgetRecordMatches(beforeReports.Metadata(), &live, r, policy) {
			return nil
		}
		err = s.enforceTestBudget(ctx, result.gh, live, result.snap, policy)
	}
	fresh, readErr := readBudgetSnapshot(s.Store.Root)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	live, ok := fresh.Record(result.key)
	if !ok || !budgetRecordMatches(fresh.Metadata(), &live, r, policy) {
		return err
	}
	if err == nil {
		err = s.saveCheckFailureExcerpts(ctx, result.gh, live, result.snap)
	}
	after, readErr := readBudgetSnapshot(s.Store.Root)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	final, ok := after.Record(result.key)
	if !ok || !budgetRecordMatches(after.Metadata(), &final, r, policy) {
		return err
	}
	budgetChanged := !budgetOutcomesEqual(r, final)
	requestsSaved = true
	saveErr := s.saveBudgetRequests(result, r, policy, after)
	if err != nil {
		err = fmt.Errorf("%s: budget check: %w", result.key, err)
	}
	var processErr error
	// Full polling defers passing-CI failure resets until budgets succeed.
	resetFailures := err == nil && (final.CI == state.CIPassed || final.CI == state.CIAbsent) && (final.Failures > 0 || final.CIPaused)
	if budgetChanged || resetFailures || s.budgetCIUnreconciled(final, result.snap, policy) {
		// A later failure cannot undo an earlier committed run's outcome.
		// Keep failure resets pending until the budget phase succeeds.
		processErr = s.processSnapshot(ctx, result, err != nil)
	}
	return errors.Join(err, saveErr, processErr)
}

func (s *Service) finishPoll(ctx context.Context, st State, result pollResult) error {
	key, gh, err := result.key, result.gh, result.err
	r := st.Records[key]
	if err == nil {
		err = ctx.Err()
	}
	entries := maps.Clone(st.Responses[key])
	if entries == nil {
		entries = map[string]cachedResponse{}
	}
	for path, response := range gh.requests.used {
		entries[path] = response
	}
	if err == nil && !result.unchanged {
		entries = gh.requests.used
	}
	unlock := gh.requests.lockRate()
	rates := cloneRates(gh.requests.rates)
	unlock()
	if saveErr := s.Store.updateRecordOrAll(key, func(current *State) bool {
		return ratesNeedAllRecords(current, rates, s.now())
	}, func(current *State) error {
		if current.Responses == nil {
			current.Responses = map[string]map[string]cachedResponse{}
		}
		// Bodies are immutable; detach the worker's mutable response entry map.
		current.Responses[key] = maps.Clone(entries)
		if live := current.Records[key]; live != nil && gh.requests.account != "" {
			live.QuotaAccount = gh.requests.account
		}
		if result.unchanged && err == nil {
			if live := current.Records[key]; live != nil && live.Watching && live.Head == r.Head {
				live.Checked = s.now()
				live.Error = ""
				live.Polling.Error = ""
				live.Polling.CIVersion = result.snap.ciProbe
				live.HeadStale = false
				live.Polling.IntervalMinutes = 1
				if reserve(quotaFor(State{RateLimits: rates}, r, s.now()), s.now()) {
					live.Polling.IntervalMinutes = 5
				}
				live.Polling.Next = nextPollTime(live.Checked, live.Polling.IntervalMinutes)
				if live.Polling.IntervalMinutes == 1 {
					live.Polling.Next = nextPollTime(result.polledAt, 1)
				}
			}
		}
		applyRateLimits(current, rates, s.now())
		return nil
	}); saveErr != nil {
		return saveErr
	}
	if err != nil {
		previousError := r.Error
		if result.snap.headConfirmed {
			previousError = r.Polling.Error
		}
		changed := previousError != Sanitize(err.Error())
		saveErr := s.Store.updateRecords([]string{key}, func(st *State) error {
			if r := st.Records[key]; r != nil {
				if result.snap.headConfirmed {
					r.Polling.Error = Sanitize(err.Error())
				} else {
					r.Error = Sanitize(err.Error())
					r.HeadStale = true
				}
				// An attempted head yields its priority even when the request fails.
				// Checked then places retries behind older records.
				if !result.snap.headConfirmed {
					r.Polling.PullHead = r.Head
				}
				r.Checked = s.now()
				r.Polling.Next = nextPollTime(result.polledAt, 1)
				var limited *rateLimitError
				if errors.As(err, &limited) {
					r.Polling.Next = limited.until
				}
			}
			return nil
		})
		if saveErr != nil {
			return saveErr
		}
		if changed {
			return fmt.Errorf("%s: %w", key, err)
		}
		return nil
	}
	if result.unchanged {
		return nil
	}
	return s.processSnapshot(ctx, result, true)
}

// processSnapshot applies a full snapshot to the stored record.
func (s *Service) processSnapshot(ctx context.Context, result pollResult, budgetsDue bool) error {
	st, err := s.readTickState(ctx)
	if err != nil {
		return err
	}
	r := st.Records[result.key]
	if r == nil {
		return nil
	}
	result.snap, err = s.withRegisteredCI(ctx, *r, result.snap, EffectivePolicy(st, *r))
	if err != nil {
		return err
	}
	state, ciKey, jobs, runs := s.ciStatus(result.snap, *r, EffectivePolicy(st, *r))
	var logs []string
	if result.snap.localCI != nil && result.snap.localCI.Logs != "" {
		logs = append(logs, logExcerpt(result.snap.localCI.Logs))
	}
	if state == "failed" && ciKey != r.CIKey && !r.CIPaused {
		for i, run := range runs {
			if i == 3 {
				break
			}
			logs = append(logs, fmt.Sprintf("%s (run %d, attempt %d):\n%s", run.Name, run.ID, run.Attempt, failureExcerpt(result.gh.Logs(ctx, refRecord(*r), run), true)))
		}
	}
	return s.process(ctx, result.key, result.snap, state, ciKey, jobs, logs, budgetsDue, result.polledAt)
}
