package auth

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The selector, its in-memory observations, the registration epoch check and the
// routing status adapt upstream PR #7020, head b7ab0c79a05d281ba4ccaa502fb3b02dae5c7c60.
// That PR takes one reset per credential from an external collector. Dyna's readers
// run in this process and report every window, so a reading here keeps all of them,
// and the ranking is earliest deadline first with gates. dyna_soonest_reset_sim_test.go
// compares it with the alternatives. See DYNA.md.
const (
	// soonestResetLongSeconds is the shortest window that sets a deadline. A rolling
	// 5-hour window comes back within the day, so its reset wastes little.
	soonestResetLongSeconds = 86400
	// SoonestResetLongFloorPercent is the quota left on a long window at or below
	// which an account takes no new session: it could not usefully start one.
	SoonestResetLongFloorPercent = 3.0
	// SoonestResetLongReservePercent is the same line for a window that resets more
	// than soonestResetReserveHorizon away. Those accounts keep enough for the sessions
	// already on them and for bursts, and a fleet that still has them keeps its 5-hour
	// throughput. Inside the horizon the reserve lifts, so it strands nothing.
	SoonestResetLongReservePercent = 10.0
	soonestResetReserveHorizon     = 24 * time.Hour
	// SoonestResetSessionCeilingPercent is the 5-hour use at or above which an account
	// takes no new session, so parallel sessions spill to the next account rather
	// than all reaching the session cap. An account on course to pass it by the window's
	// reset sorts after other running accounts, because bound sessions keep running and
	// a reading can be ten minutes old. It still comes before an unstarted account: a
	// clock started early expires half used, which wastes more than a session cap costs.
	SoonestResetSessionCeilingPercent = 90.0
	// soonestResetPaceMinimum is how long a 5-hour window must have run for its pace to
	// mean anything.
	soonestResetPaceMinimum = 30 * time.Minute
	// SoonestResetRefreshInterval is the least time between reads a cold pick starts
	// for one provider. A reading younger than this counts as current. Reset times do
	// not move, and a real 429 cools the account anyway, so a reading only has to keep
	// the gates near the truth. See DYNA.md for why ten minutes.
	SoonestResetRefreshInterval = 10 * time.Minute
	// soonestResetRefreshCeiling bounds the wait after repeated 429s from a reader.
	soonestResetRefreshCeiling = 2 * time.Hour
	// soonestResetMaxAge ends a reading whose reader keeps failing. It outlasts the
	// longest backoff, so one failed refresh does not move an account.
	soonestResetMaxAge = 6 * time.Hour
)

// Classes of a ranked account, best first.
const (
	// soonestResetRunning has quota on a clock: unused quota is lost at the deadline.
	soonestResetRunning = iota
	// soonestResetUnstarted has a long window that has not started: it can wait.
	soonestResetUnstarted
	// soonestResetUnknown has no current reading: order among these is round robin.
	soonestResetUnknown
	// soonestResetPaid costs real money: API keys and prepaid balances.
	soonestResetPaid
)

// QuotaResetWindow is one usage window of a reading. Model names a model-scoped
// window. A zero ResetAt is a window with no running clock.
type QuotaResetWindow struct {
	Model       string
	Seconds     int64
	UsedPercent float64
	ResetAt     time.Time
}

// QuotaResetHint is a non-secret reading of one credential. It stays in memory and is
// never persisted with OAuth credentials.
type QuotaResetHint struct {
	ObservedAt time.Time
	Windows    []QuotaResetWindow
	// Prepaid marks a balance reading, and Empty one with no credit left.
	Prepaid bool
	Empty   bool

	// registrationEpoch binds a retained observation to the registration it was made
	// for. File-backed IDs are path-based, so a credential removed and registered
	// again under the same ID is a different account and must not inherit the reading.
	registrationEpoch uint64
}

func (h QuotaResetHint) current(now time.Time) bool {
	return !h.ObservedAt.IsZero() && !h.ObservedAt.After(now) && now.Sub(h.ObservedAt) < soonestResetMaxAge
}

// quotaResetBoard holds readings and rate-limits the refreshes cold picks ask for. It
// lives on the Manager, so a selector swap on hot reload keeps it, and a Pick reaches
// it through the context. It never reads a provider itself.
type quotaResetBoard struct {
	mu       sync.RWMutex
	hints    map[string]QuotaResetHint
	refresh  func(provider string) (rateLimited bool)
	next     map[string]time.Time
	failures map[string]int
	running  map[string]bool
}

func (b *quotaResetBoard) hint(auth *Auth) (QuotaResetHint, bool) {
	if b == nil || auth == nil {
		return QuotaResetHint{}, false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	hint, ok := b.hints[auth.ID]
	if !ok || hint.registrationEpoch != auth.RegistrationEpoch {
		return QuotaResetHint{}, false
	}
	return hint, true
}

// needsReading reports whether a pick should ask for a newer reading of auth: it has
// none, the reading is old, or a reset in it has passed.
func (b *quotaResetBoard) needsReading(auth *Auth, now time.Time) bool {
	hint, ok := b.hint(auth)
	if !ok || now.Sub(hint.ObservedAt) >= SoonestResetRefreshInterval {
		return true
	}
	for _, window := range hint.Windows {
		if !window.ResetAt.IsZero() && !window.ResetAt.After(now) {
			return true
		}
	}
	return false
}

// requestRefresh starts at most one read per provider and returns at once. A provider
// waits SoonestResetRefreshInterval between reads, and longer after a 429.
func (b *quotaResetBoard) requestRefresh(auths []*Auth, now time.Time) {
	if b == nil {
		return
	}
	asked := make(map[string]struct{})
	for _, auth := range auths {
		if auth == nil || !b.needsReading(auth, now) {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if _, seen := asked[provider]; !seen {
			asked[provider] = struct{}{}
			b.startRefresh(provider, now)
		}
	}
}

func (b *quotaResetBoard) startRefresh(provider string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refresh == nil || b.running[provider] || now.Before(b.next[provider]) {
		return
	}
	if b.running == nil {
		b.running, b.next, b.failures = make(map[string]bool), make(map[string]time.Time), make(map[string]int)
	}
	b.running[provider] = true
	b.next[provider] = now.Add(SoonestResetRefreshInterval)
	go b.runRefresh(provider, b.refresh, now)
}

func (b *quotaResetBoard) runRefresh(provider string, refresh func(string) bool, started time.Time) {
	rateLimited := false
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Errorf("soonest-reset: usage refresh panicked: %v", recovered)
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.running[provider] = false
		if !rateLimited {
			delete(b.failures, provider)
			return
		}
		b.failures[provider]++
		wait := soonestResetRefreshCeiling
		if shift := b.failures[provider]; shift < 8 {
			wait = min(SoonestResetRefreshInterval<<shift, soonestResetRefreshCeiling)
		}
		b.next[provider] = started.Add(wait)
	}()
	rateLimited = refresh(provider)
}

// PublishQuotaReading records a reader's result for a credential. Everything that
// reads usage publishes here, so a read Home started serves routing too. A failed read
// publishes nothing, which leaves the last reading in place.
func (m *Manager) PublishQuotaReading(authID string, hint QuotaResetHint) {
	if m == nil {
		return
	}
	m.mu.RLock()
	auth := m.auths[authID]
	if auth == nil {
		m.mu.RUnlock()
		return
	}
	hint.registrationEpoch = auth.RegistrationEpoch
	m.mu.RUnlock()
	board := &m.quotaBoard
	board.mu.Lock()
	defer board.mu.Unlock()
	if current, ok := board.hints[authID]; ok && current.registrationEpoch == hint.registrationEpoch && current.ObservedAt.After(hint.ObservedAt) {
		return
	}
	if board.hints == nil {
		board.hints = make(map[string]QuotaResetHint)
	}
	board.hints[authID] = hint
}

// SetQuotaRefresher installs the reader that cold picks start. refresh reports whether
// the provider answered 429. It runs on its own goroutine, never on a request.
func (m *Manager) SetQuotaRefresher(refresh func(provider string) (rateLimited bool)) {
	if m == nil {
		return
	}
	m.quotaBoard.mu.Lock()
	m.quotaBoard.refresh = refresh
	m.quotaBoard.mu.Unlock()
}

// soonestResetPolicy is the line a cold pick will not cross. The zero value has no
// gates, which is what failover uses, so an account can finish its last few percent.
type soonestResetPolicy struct {
	floor, reserve, sessionCeiling float64
	reserveHorizon                 time.Duration
	pace                           bool
}

var soonestResetGates = soonestResetPolicy{
	floor:          SoonestResetLongFloorPercent,
	reserve:        SoonestResetLongReservePercent,
	reserveHorizon: soonestResetReserveHorizon,
	sessionCeiling: SoonestResetSessionCeilingPercent,
	pace:           true,
}

func (p soonestResetPolicy) gatesLong(window *QuotaResetWindow, left float64, now time.Time) bool {
	if left <= p.floor {
		return true
	}
	return left <= p.reserve && (window.ResetAt.IsZero() || window.ResetAt.Sub(now) > p.reserveHorizon)
}

func (p soonestResetPolicy) gatesShort(window *QuotaResetWindow) bool {
	return p.sessionCeiling > 0 && window.UsedPercent >= p.sessionCeiling
}

func (p soonestResetPolicy) pacedShort(window *QuotaResetWindow, now time.Time) bool {
	if !p.pace || window.ResetAt.IsZero() || window.Seconds <= 0 {
		return false
	}
	length := time.Duration(window.Seconds) * time.Second
	elapsed := length - window.ResetAt.Sub(now)
	return elapsed >= soonestResetPaceMinimum && window.UsedPercent*float64(length)/float64(elapsed) >= p.sessionCeiling
}

// soonestResetRank is what one account's reading says at one moment.
type soonestResetRank struct {
	class    int
	deadline time.Time
	// left is the quota left on the limiting window, times the plan's size.
	left float64
	// gated accounts take no new session.
	gated bool
	// paced accounts are on course for the session ceiling.
	paced bool
	// spent accounts have nothing left on an applicable window or balance. They come
	// last even on failover, and those that recover soonest come first.
	spent    bool
	recovers time.Time
}

// rankSoonestReset applies the reading to one model. An empty model applies only the
// account's own windows.
func rankSoonestReset(auth *Auth, hint QuotaResetHint, known bool, model string, now time.Time, policy soonestResetPolicy) soonestResetRank {
	rank := soonestResetRank{class: soonestResetUnknown}
	if auth != nil && auth.AuthKind() == AuthKindAPIKey {
		rank.class = soonestResetPaid
	}
	if !known || !hint.current(now) {
		return rank
	}
	var running, started, short *QuotaResetWindow
	long, stale := false, false
	for i := range hint.Windows {
		window := &hint.Windows[i]
		if !quotaWindowApplies(window, model) {
			continue
		}
		passed := !window.ResetAt.IsZero() && !window.ResetAt.After(now)
		isLong := window.Seconds >= soonestResetLongSeconds
		if passed {
			// A passed reset means the reading no longer describes this window. A passed
			// 5-hour reset says nothing about the week, so only a long one ends the reading.
			stale = stale || isLong
			continue
		}
		left := 100 - math.Round(window.UsedPercent)
		if left <= 0 {
			rank.spent = true
			if window.ResetAt.After(rank.recovers) {
				rank.recovers = window.ResetAt
			}
		}
		if !isLong {
			rank.gated = rank.gated || policy.gatesShort(window)
			rank.paced = rank.paced || policy.pacedShort(window, now)
			if short == nil || window.Seconds > short.Seconds {
				short = window
			}
			continue
		}
		long = true
		rank.gated = rank.gated || policy.gatesLong(window, left, now)
		switch {
		case !window.ResetAt.IsZero():
			// The deadline is the reset of the window with the least left.
			if running == nil || window.UsedPercent > running.UsedPercent ||
				(window.UsedPercent == running.UsedPercent && window.ResetAt.Before(running.ResetAt)) {
				running = window
			}
		case window.UsedPercent > 0:
			started = window
		}
	}
	if stale {
		return soonestResetRank{class: rank.class}
	}
	if hint.Empty {
		rank.spent = true
	}
	size := dynaPlanSize(auth)
	switch {
	case running != nil:
		rank.class, rank.deadline = soonestResetRunning, running.ResetAt
		rank.left = (100 - running.UsedPercent) * size
	case long && started == nil:
		rank.class, rank.left = soonestResetUnstarted, 100*size
	case !long && short != nil && !short.ResetAt.IsZero():
		// Without a long window the short one is the account's only limit.
		rank.class, rank.deadline = soonestResetRunning, short.ResetAt
		rank.left = (100 - short.UsedPercent) * size
	case hint.Prepaid:
		rank.class = soonestResetPaid
	}
	return rank
}

// quotaWindowApplies reports whether a window limits a model. A model-scoped window
// applies only to its model; an unnamed scope matches nothing, so it cannot block.
func quotaWindowApplies(window *QuotaResetWindow, model string) bool {
	if window.Model == "" {
		return true
	}
	scope := quotaModelText(window.Model)
	return scope != "" && strings.Contains(quotaModelText(canonicalModelKey(model)), scope)
}

func quotaModelText(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		default:
			return -1
		}
	}, text)
}

var dynaPlanTier = regexp.MustCompile(`max[ _-]?(\d+)x`)

// dynaPlanSize is what one percent of this account is worth, when the credential says:
// Max 20x is 20 and Max 5x is 5. Claude OAuth records carry no plan, so they count 1.
func dynaPlanSize(auth *Auth) float64 {
	if auth == nil {
		return 1
	}
	for _, key := range []string{"plan_type", "plan"} {
		text := strings.ToLower(auth.Attributes[key])
		if text == "" {
			text, _ = auth.Metadata[key].(string)
			text = strings.ToLower(text)
		}
		if match := dynaPlanTier.FindStringSubmatch(text); match != nil {
			if size, err := strconv.ParseFloat(match[1], 64); err == nil && size > 0 {
				return size
			}
		}
	}
	return 1
}

// soonestResetFirst orders two ranks. tied means a and b share a place, which only
// accounts with nothing to tell them apart do.
func soonestResetFirst(a, b soonestResetRank, aID, bID string) (first, tied bool) {
	if a.spent != b.spent {
		return b.spent, false
	}
	if a.spent {
		if !a.recovers.Equal(b.recovers) {
			return a.recovers.Before(b.recovers), false
		}
		return false, true
	}
	if a.gated != b.gated {
		return b.gated, false
	}
	if a.class != b.class {
		return a.class < b.class, false
	}
	if a.class >= soonestResetUnknown {
		return false, true
	}
	if a.paced != b.paced {
		return b.paced, false
	}
	if !a.deadline.Equal(b.deadline) {
		return a.deadline.Before(b.deadline), false
	}
	if a.left != b.left {
		return a.left > b.left, false
	}
	return aID < bID, false
}

// soonestResetPlace is one rung of the order: one account, or several that share it.
type soonestResetPlace struct {
	auths []*Auth
	rank  soonestResetRank
}

// soonestResetOrder is the one ranking: a Pick takes its first place, and the routing
// status, and so the active account Home shows, reports the same places. Accounts past a
// gate sort after every other account that is not spent, so when nobody clears the
// gates the first place is the best of those rather than a failure.
func soonestResetOrder(board *quotaResetBoard, auths []*Auth, model string, now time.Time, policy soonestResetPolicy) []soonestResetPlace {
	type entry struct {
		auth *Auth
		rank soonestResetRank
	}
	entries := make([]entry, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		hint, known := board.hint(auth)
		entries = append(entries, entry{auth, rankSoonestReset(auth, hint, known, model, now, policy)})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		first, _ := soonestResetFirst(entries[i].rank, entries[j].rank, entries[i].auth.ID, entries[j].auth.ID)
		return first
	})
	places := make([]soonestResetPlace, 0, len(entries))
	for i, item := range entries {
		if i > 0 {
			_, tied := soonestResetFirst(entries[i-1].rank, item.rank, entries[i-1].auth.ID, item.auth.ID)
			if tied {
				last := &places[len(places)-1]
				last.auths = append(last.auths, item.auth)
				continue
			}
		}
		places = append(places, soonestResetPlace{auths: []*Auth{item.auth}, rank: item.rank})
	}
	return places
}

type soonestResetBoardKey struct{}
type soonestResetFailoverKey struct{}

// withSoonestReset hands a selector the board and says whether this pick replaces a
// credential that failed the request.
func (m *Manager) withSoonestReset(ctx context.Context, tried map[string]struct{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, soonestResetBoardKey{}, &m.quotaBoard)
	if len(tried) > 0 {
		ctx = withSoonestResetFailover(ctx)
	}
	return ctx
}

func withSoonestResetFailover(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, soonestResetFailoverKey{}, true)
}

func soonestResetFailover(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	failover, _ := ctx.Value(soonestResetFailoverKey{}).(bool)
	return failover
}

func soonestResetBoardFrom(ctx context.Context) *quotaResetBoard {
	if ctx == nil {
		return nil
	}
	board, _ := ctx.Value(soonestResetBoardKey{}).(*quotaResetBoard)
	return board
}

// SoonestResetSelector sends new work to the account whose quota expires first, so
// quota is spent before it is lost at reset. Session affinity wraps it, and it runs on
// the manager's route-aware path, so aliases, retries and model cooldowns are settled
// before it compares readings. Pick does no I/O: it reads memory and may start one
// background refresh.
type SoonestResetSelector struct {
	fallback WeightedRoundRobinSelector
	now      func() time.Time
}

func (s *SoonestResetSelector) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Pick selects the first place of the order. Accounts that share it, which have no
// reading or are paid, share by weight, which is round robin at the default weight.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := s.clock()
	available, err := getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	if pinned := dynaPinnedAuth(ctx, available, model, now); pinned != nil {
		return pinned, nil
	}
	board := soonestResetBoardFrom(ctx)
	board.requestRefresh(available, now)
	policy := soonestResetGates
	if soonestResetFailover(ctx) {
		policy = soonestResetPolicy{}
	}
	places := soonestResetOrder(board, available, model, now, policy)
	if len(places[0].auths) == 1 {
		return places[0].auths[0], nil
	}
	return s.fallback.Pick(ctx, provider, model, opts, places[0].auths)
}

// selectorUsesQuotaWeights reports a selector that shares by credential weight, so a
// non-positive weight takes a credential out of its candidates.
func selectorUsesQuotaWeights(selector Selector) bool {
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		selector = affinity.fallback
	}
	switch selector.(type) {
	case *WeightedRoundRobinSelector, *SoonestResetSelector:
		return true
	default:
		return false
	}
}

// Routing states of a credential in the routing order.
const (
	RoutingStateEligible    = "eligible"
	RoutingStateZeroWeight  = "zero_weight"
	RoutingStateCooldown    = "cooldown"
	RoutingStateUnavailable = "unavailable"
	RoutingStateDisabled    = "disabled"
)

// RoutingAccountView is one credential in the routing order. Rank 1 receives new work;
// credentials with one rank share it. Ineligible credentials have no rank.
type RoutingAccountView struct {
	AuthIndex string     `json:"auth_index"`
	Rank      int        `json:"rank,omitempty"`
	Shared    bool       `json:"shared,omitempty"`
	Spent     bool       `json:"spent,omitempty"`
	Gated     bool       `json:"gated,omitempty"`
	State     string     `json:"state"`
	Deadline  *time.Time `json:"deadline,omitempty"`
}

// QuotaRoutingSnapshot is a read-only view of the order, keyed by management auth index,
// never by credential ID, path or token. Providers is empty under any other strategy.
type QuotaRoutingSnapshot struct {
	Strategy    string                          `json:"strategy"`
	GeneratedAt time.Time                       `json:"generated_at"`
	Providers   map[string][]RoutingAccountView `json:"providers,omitempty"`
}

// QuotaRoutingSnapshot reports the order a cold pick for no particular model would use.
func (m *Manager) QuotaRoutingSnapshot() QuotaRoutingSnapshot {
	now := time.Now()
	snapshot := QuotaRoutingSnapshot{Strategy: "unknown", GeneratedAt: now.UTC()}
	if m == nil {
		return snapshot
	}
	m.mu.RLock()
	selector := m.selector
	auths := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		auths = append(auths, auth.Clone())
	}
	m.mu.RUnlock()
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		selector = affinity.fallback
	}
	if _, ok := selector.(*SoonestResetSelector); ok {
		snapshot.Strategy = "soonest-reset"
		snapshot.Providers = routingOrder(&m.quotaBoard, auths, now)
	}
	return snapshot
}

// routingOrder ranks credentials per provider with the selector's rules: strict priority
// tiers, then soonestResetOrder inside a tier. Model cooldowns, transport preference and
// session affinity still apply to individual requests.
func routingOrder(board *quotaResetBoard, auths []*Auth, now time.Time) map[string][]RoutingAccountView {
	pools := make(map[string][]*Auth)
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		// Group exactly like request selection, so provider aliases that share a
		// scheduling pool rank together.
		if provider := canonicalSchedulingProvider(executorKeyFromAuth(auth)); provider != "" {
			pools[provider] = append(pools[provider], auth)
		}
	}
	out := make(map[string][]RoutingAccountView, len(pools))
	for provider, pool := range pools {
		sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
		views := make([]RoutingAccountView, 0, len(pool))
		eligible := make([]*Auth, 0, len(pool))
		for _, auth := range pool {
			blocked, reason, _ := isAuthBlockedForModel(auth, "", now)
			view := RoutingAccountView{AuthIndex: auth.Index}
			switch {
			case blocked && reason == blockReasonDisabled:
				view.State = RoutingStateDisabled
			case blocked && reason == blockReasonCooldown:
				view.State = RoutingStateCooldown
			case blocked:
				view.State = RoutingStateUnavailable
			case authWeight(auth) <= 0:
				view.State = RoutingStateZeroWeight
			default:
				eligible = append(eligible, auth)
				continue
			}
			views = append(views, view)
		}
		rank := 0
		for len(eligible) > 0 {
			tier := highestPriorityAuths(eligible)
			chosen := make(map[string]struct{}, len(tier))
			for _, place := range soonestResetOrder(board, tier, "", now, soonestResetGates) {
				rank++
				for _, auth := range place.auths {
					chosen[auth.ID] = struct{}{}
					view := RoutingAccountView{AuthIndex: auth.Index, Rank: rank, Shared: len(place.auths) > 1, Spent: place.rank.spent, Gated: place.rank.gated, State: RoutingStateEligible}
					if !place.rank.deadline.IsZero() {
						deadline := place.rank.deadline.UTC()
						view.Deadline = &deadline
					}
					views = append(views, view)
				}
			}
			remaining := make([]*Auth, 0, len(eligible))
			for _, auth := range eligible {
				if _, ok := chosen[auth.ID]; !ok {
					remaining = append(remaining, auth)
				}
			}
			eligible = remaining
		}
		sort.SliceStable(views, func(i, j int) bool {
			if (views[i].Rank > 0) != (views[j].Rank > 0) {
				return views[i].Rank > 0
			}
			return views[i].Rank < views[j].Rank
		})
		out[provider] = views
	}
	return out
}
