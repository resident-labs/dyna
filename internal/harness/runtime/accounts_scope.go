package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/repoid"
)

var errScopeRefused = errors.New("The repo rules refuse the active account")

// SetRepos replaces the repo list of an account. An empty list lets the
// account run in any repo that no other account names.
func (a *Accounts) SetRepos(id string, repos []string) error {
	s, err := a.scopes()
	if err != nil {
		return err
	}
	return s.Set(id, repos)
}

func (a *Accounts) scopes() (*reposcope.Store, error) { return reposcope.Open(filepath.Dir(a.Root)) }

// rules reads every account's repo list from the data directory that holds Root.
func (a *Accounts) rules() (reposcope.Rules, error) {
	s, err := a.scopes()
	if err != nil {
		return nil, err
	}
	return s.Rules()
}

// RepoIdentity names the repository of dir by its origin remote. It returns ""
// when dir has no origin, so such a turn counts as having no repository.
func RepoIdentity(ctx context.Context, dir string) string {
	out, err := gitexec.Command(ctx, dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	id, err := repoid.RemoteIdentity(dir, strings.TrimSpace(string(out)))
	if err != nil {
		return ""
	}
	return id
}

func repoName(repo string) string {
	if repo == "" {
		return "a directory without a repository"
	}
	return repo
}

// SelectForRepo makes the active account of provider one that harness h may run
// and that may run in repo. It keeps the active account when allowed. Otherwise
// it switches to an allowed account with quota left, ranked by its usage score,
// and never picks one whose reading says it is at a cap. It never picks an
// account the repo rules refuse. With no scopes saved it does nothing.
func (a *Accounts) SelectForRepo(ctx context.Context, harness string, provider store.Provider, roomID, repo, executable string) error {
	rules, err := a.rules()
	if err != nil || len(rules) == 0 {
		return err
	}
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return err
	}
	if provider == "" {
		return nil
	}
	all, err := a.List()
	if err != nil {
		return err
	}
	// The account this turn would run on now keeps its place when the repo
	// allows it, so a room or provider selection is not replaced needlessly.
	current, _ := a.activeAccount(harness, provider, roomID)
	var allowed []NativeAccount
	for _, n := range all {
		if accountRuns(h, n, provider) && n.Status == "ready" && rules.Allows(n.ID, repo) {
			if n.ID == current.ID {
				return nil
			}
			allowed = append(allowed, n)
		}
	}
	if len(allowed) == 0 {
		return fmt.Errorf("No signed-in %s account may run in %s. Allow one in Settings, Accounts.", provider, repoName(repo))
	}
	now := time.Now()
	earliest, earliestLabel := time.Time{}, ""
	open := allowed
	var limits, stated map[string]time.Time
	if scopes, e := a.scopes(); e == nil {
		stated, _ = scopes.Stated(now)
		if limits, e = scopes.Limits(now); e == nil {
			open = nil
			for _, n := range allowed {
				if end, hit := limits[n.ID]; hit {
					if earliest.IsZero() || end.Before(earliest) {
						earliest, earliestLabel = end, n.Label
					}
					continue
				}
				open = append(open, n)
			}
		}
	}
	chosen, earliest, earliestLabel := a.chooseUsage(ctx, open, earliest, earliestLabel, now)
	if chosen == nil {
		if earliest.IsZero() {
			return fmt.Errorf("Every %s account that may run in %s is at its usage limit.", provider, repoName(repo))
		}
		return fmt.Errorf("No %s account with quota left may run in %s. %s", provider, repoName(repo), resetSentence(earliestLabel, earliest, limits, stated))
	}
	return a.Switch(ctx, chosen.ID, executable)
}

// activeFor returns the account provider uses in roomID when the repo rules
// allow it. A turn calls it after it holds its lease, so a switch by another
// turn cannot move this one onto an account that the repo refuses. The room's
// own selection wins over the global one.
func (a *Accounts) activeFor(harness string, provider store.Provider, roomID, repo string) (NativeAccount, error) {
	active, err := a.activeAccount(harness, provider, roomID)
	if err != nil {
		return active, err
	}
	rules, err := a.rules()
	if err != nil {
		return active, err
	}
	if !rules.Allows(active.ID, repo) {
		return active, fmt.Errorf("%w: the active %s account %s may not run in %s. Try the turn again.", errScopeRefused, active.Provider, active.Label, repoName(repo))
	}
	return active, nil
}

// SelectAccount runs before a turn in dir. With no scopes saved it changes
// nothing. Otherwise it records the repo on the returned profile and switches
// to an account that may run there.
func (r *Runtime) SelectAccount(ctx context.Context, p profile.Profile, dir string) (profile.Profile, error) {
	rules, err := r.Accounts.rules()
	if err != nil || len(rules) == 0 {
		return p, err
	}
	p.RuntimeRepo = RepoIdentity(ctx, dir)
	// A first turn has no copy of the harness yet; install it as prepare does.
	b, _, err := (&FirstUse{Bundles: r.Bundles}).EnsureBundle(ctx, p.Harness, p.RuntimeVersion)
	if err != nil {
		return p, err
	}
	return p, r.Accounts.SelectForRepo(ctx, p.Harness, ProviderForModel(p.Harness, p.Model), p.Env[mode.EnvRoomID], p.RuntimeRepo, b.Executable)
}

// resetSentence says when label's account resets. A time that is only a stored
// guess (in limits, not in stated) is never shown: the sentence says the
// provider named no time. A credit hold has no end, so it says so.
func resetSentence(label string, at time.Time, limits, stated map[string]time.Time) string {
	if at.Equal(reposcope.OpenEnded) {
		return label + " has no reset time; it runs again after credits or a successful check."
	}
	guess := false
	for _, end := range limits {
		guess = guess || end.Equal(at)
	}
	for _, end := range stated {
		if end.Equal(at) {
			guess = false
		}
	}
	if guess {
		return label + " reports no reset time."
	}
	return fmt.Sprintf("%s resets %s.", label, at.Local().Format("Mon 15:04"))
}

// Failover runs after a turn on the account failedID hit a usage limit that
// lasts until the given time. It remembers that limit, then picks another
// account of the same provider that the repo allows, is signed in and has quota
// left: usage readings rank the candidates, the chosen reading is refreshed
// first, and an account whose reading says it has no quota left is never
// picked. The turn's own account fails, not whatever is active now: another
// turn may have switched since. The active account is kept when it qualifies.
// It never picks an account the repo rules refuse. With none left, it names
// the earliest reset.
func (a *Accounts) Failover(ctx context.Context, provider store.Provider, failedID, repo, executable string, hold reposcope.Hold) (from, to NativeAccount, err error) {
	if from, err = a.Find(failedID); err != nil {
		return from, to, err
	}
	scopes, err := a.scopes()
	if err != nil {
		return from, to, err
	}
	if err = scopes.Hold(from.ID, hold); err != nil {
		return from, to, err
	}
	rules, err := scopes.Rules()
	if err != nil {
		return from, to, err
	}
	limits, err := scopes.Limits(time.Now())
	if err != nil {
		return from, to, err
	}
	statedLimits, err := scopes.Stated(time.Now())
	if err != nil {
		return from, to, err
	}
	all, err := a.List()
	if err != nil {
		return from, to, err
	}
	// The stored limit is the later of the old and new ends, so it is the reset.
	earliest, earliestLabel := hold.Until, from.Label
	if end, hit := limits[from.ID]; hit {
		earliest = end
	}
	var open []NativeAccount
	for _, n := range all {
		if !accountRuns(from.Harness, n, provider) || n.ID == from.ID || n.Status != "ready" || !rules.Allows(n.ID, repo) {
			continue
		}
		if end, hit := limits[n.ID]; hit {
			if end.Before(earliest) {
				earliest, earliestLabel = end, n.Label
			}
			continue
		}
		open = append(open, n)
	}
	chosen, earliest, earliestLabel := a.chooseUsage(ctx, open, earliest, earliestLabel, time.Now())
	if chosen == nil {
		return from, to, fmt.Errorf("No other %s account may run in %s with quota left. %s", provider, repoName(repo), resetSentence(earliestLabel, earliest, limits, statedLimits))
	}
	if chosen.Active {
		return from, *chosen, nil
	}
	to = *chosen
	return from, to, a.Switch(ctx, to.ID, executable)
}

// FiveHourWindow is the store ID of the five-hour usage window that Kimi reports.
const FiveHourWindow = "five_hour"

// LimitOf returns the account's stored usage-limit hold, ended or not.
func (a *Accounts) LimitOf(id string) (reposcope.Hold, error) {
	scopes, err := a.scopes()
	if err != nil {
		return reposcope.Hold{}, err
	}
	return scopes.LimitOf(id)
}

// ClearLimit removes the account's hold, because a turn on it succeeded.
func (a *Accounts) ClearLimit(id string) error {
	scopes, err := a.scopes()
	if err != nil {
		return err
	}
	return scopes.ClearLimit(id)
}

// clearProbedHold clears an account's unstated hold after a successful usage
// probe that began at since. A failed clear only logs, because the probe itself
// succeeded.
func (a *Accounts) clearProbedHold(id string, since time.Time) {
	scopes, err := a.scopes()
	if err == nil {
		err = scopes.ClearUnstated(id, since)
	}
	if err != nil {
		log.Printf("clear quota hold for account %s: %v", id, err)
	}
}

// clearRefreshedHold clears an account's no-timer hold after a successful
// credential refresh that began at since. A failed clear only logs.
func (a *Accounts) clearRefreshedHold(id string, since time.Time) {
	scopes, err := a.scopes()
	if err == nil {
		err = scopes.ClearOpenEnded(id, since)
	}
	if err != nil {
		log.Printf("clear quota hold for account %s: %v", id, err)
	}
}

// WindowReset probes the account's usage and returns when the named window
// resets. It returns zero when the probe fails or the window has no reset.
func (a *Accounts) WindowReset(ctx context.Context, id, windowID string) time.Time {
	n, err := a.Find(id)
	if err != nil {
		return time.Time{}
	}
	observation := a.refreshReading(ctx, n)
	if observation == nil {
		return time.Time{}
	}
	for _, w := range observation.Windows {
		if reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt); err == nil && w.ID == windowID && reset.After(time.Now()) {
			return reset
		}
	}
	return time.Time{}
}

// usageProbeTimeout bounds one saved-usage probe during an account choice.
const usageProbeTimeout = 20 * time.Second

// chooseUsage picks the account an automatic switch moves to. Saved usage
// readings rank the candidates with store.Score: highest score first, label
// as tie-break. The chosen candidate's reading is refreshed before it is
// accepted, so a stale near-cap reading cannot win, and an account whose
// reading says it has no quota left is never picked. The active account is
// refreshed first and kept when it stays eligible, because a switch drains
// every native process of the harness. earliest and earliestLabel name the
// first recovery for the caller's error when nothing can be chosen.
func (a *Accounts) chooseUsage(ctx context.Context, candidates []NativeAccount, earliest time.Time, earliestLabel string, now time.Time) (*NativeAccount, time.Time, string) {
	readings := map[string]*store.Observation{}
	if len(candidates) > 0 {
		ids := make([]string, len(candidates))
		for i, n := range candidates {
			ids[i] = n.ID
		}
		if found, err := a.readObservations(ids); err == nil {
			readings = found
		}
	}
	// An ineligible reading removes its account. An eligible reading without a
	// numeric score leaves the account pickable, ranked last.
	score := func(o *store.Observation) float64 {
		decision := store.DecideUsage(o, now)
		if !decision.Eligible {
			return -1
		}
		if decision.Score < 0 {
			return 0
		}
		return decision.Score
	}
	type ranked struct {
		n     NativeAccount
		score float64
	}
	order := make([]ranked, len(candidates))
	for i, n := range candidates {
		order[i] = ranked{n, score(readings[n.ID])}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].n.Label < order[j].n.Label
	})
	note := func(o *store.Observation, label string) {
		if o == nil {
			return
		}
		at := time.Time{}
		if o.RateLimitedUntil != nil {
			at = *o.RateLimitedUntil
		}
		for _, w := range o.Windows {
			if reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt); err == nil && reset.After(now) && (at.IsZero() || reset.Before(at)) {
				at = reset
			}
		}
		if !at.IsZero() && (earliest.IsZero() || at.Before(earliest)) {
			earliest, earliestLabel = at, label
		}
	}
	// refresh re-probes one candidate that has a reading, so the choice never
	// trusts a stale one. A probe that cannot run or fails keeps the saved
	// reading: a probe outage must not strand the turn without an account.
	refresh := func(n NativeAccount) *store.Observation {
		if saved := readings[n.ID]; saved == nil {
			return nil
		} else if fresh := a.refreshReading(ctx, n); fresh != nil {
			return fresh
		} else {
			return saved
		}
	}
	for _, top := range order {
		if !top.n.Active {
			continue
		}
		if fresh := refresh(top.n); score(fresh) >= 0 {
			kept := top.n
			return &kept, earliest, earliestLabel
		} else {
			note(fresh, top.n.Label)
		}
		break
	}
	for _, top := range order {
		if top.n.Active {
			continue
		}
		if top.score < 0 {
			note(readings[top.n.ID], top.n.Label)
			continue
		}
		if fresh := refresh(top.n); score(fresh) < 0 {
			note(fresh, top.n.Label)
			continue
		}
		kept := top.n
		return &kept, earliest, earliestLabel
	}
	return nil, earliest, earliestLabel
}

// readObservations reads the saved usage observation of each named account.
func (a *Accounts) readObservations(ids []string) (map[string]*store.Observation, error) {
	readings := map[string]*store.Observation{}
	err := a.transaction(func(s *store.State) error {
		for _, id := range ids {
			if entry := s.Find(id); entry != nil {
				readings[id] = entry.Observation
			}
		}
		return nil
	}, false)
	return readings, err
}

// refreshReading re-probes one account's saved usage and records the fresh
// observation. It returns nil when the account's harness has no usage probe
// or the probe failed; the saved reading then stands.
func (a *Accounts) refreshReading(ctx context.Context, n NativeAccount) *store.Observation {
	probeStart := time.Now()
	m, err := a.SavedManager(n.ID, "")
	if err != nil {
		return nil
	}
	prober, ok := m.(native.SavedProber)
	if !ok {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, usageProbeTimeout)
	defer cancel()
	captured, err := m.Capture(pctx, "")
	if err != nil || len(captured) == 0 {
		return nil
	}
	observation, err := prober.ProbeSaved(pctx, captured[0])
	if err != nil || len(observation.Windows) == 0 {
		return nil
	}
	observation.LastAttemptAt = time.Now().UTC()
	if err = a.transaction(func(s *store.State) error {
		if entry := s.Find(n.ID); entry != nil && entry.Identity == n.Identity && entry.Harness == n.Harness {
			entry.Observation = &observation
		}
		return nil
	}, true); err != nil {
		return nil
	}
	a.clearProbedHold(n.ID, probeStart)
	return &observation
}
