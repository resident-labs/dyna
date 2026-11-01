package auth

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// A deterministic comparison of account orders. Each account has a weekly and a 5-hour
// cap. A window's clock starts at first use, and whatever the week left unused at reset
// is wasted. Sessions stay bound to their account, as affinity does, and move only when
// an account refuses them. An order sees only readings, refreshed every simStepRead, and
// the cooldown a refusal caused; it never sees the true figures.
const (
	simStep         = 5 * time.Minute
	simWeek         = 7 * 24 * time.Hour
	simHorizon      = 4 * simWeek
	simUnitsPerPlan = 100.0
	simSeeds        = 12
)

type simAccount struct {
	id                 string
	plan, fiveShare    float64
	weekUsed, fiveUsed float64
	weekReset          time.Time
	fiveReset          time.Time
	cooled             time.Time
	seen               QuotaResetHint
}

func (a *simAccount) weekCap() float64 { return a.plan * simUnitsPerPlan }
func (a *simAccount) fiveCap() float64 { return a.weekCap() * a.fiveShare }

func (a *simAccount) read(now time.Time) {
	a.seen = QuotaResetHint{ObservedAt: now, Windows: []QuotaResetWindow{
		{Seconds: 18000, UsedPercent: 100 * a.fiveUsed / a.fiveCap(), ResetAt: a.fiveReset},
		{Seconds: 604800, UsedPercent: 100 * a.weekUsed / a.weekCap(), ResetAt: a.weekReset},
	}}
}

type simSession struct {
	start, end time.Time
	rate       float64
	bound      *simAccount
}

type simResult struct {
	due, wasted, demand, unmet      float64
	failedOver, sessionCaps, failed float64
	weeklyCaps                      float64
}

func (r *simResult) add(o simResult) {
	r.due += o.due
	r.wasted += o.wasted
	r.demand += o.demand
	r.unmet += o.unmet
	r.failedOver += o.failedOver
	r.sessionCaps += o.sessionCaps
	r.failed += o.failed
	r.weeklyCaps += o.weeklyCaps
}

// simOrder chooses among accounts that are not cooling. cold is false on failover.
type simOrder struct {
	name string
	pick func(state *simRotation, accounts []*simAccount, now time.Time, cold bool) *simAccount
}

type simRotation struct{ last string }

func simRoundRobin(state *simRotation, accounts []*simAccount, _ time.Time, _ bool) *simAccount {
	for _, account := range accounts {
		if account.id > state.last {
			state.last = account.id
			return account
		}
	}
	state.last = accounts[0].id
	return accounts[0]
}

// simV1Score is v1's DecideUsage: a window at 90% skips the account, and the rest score
// spare weekly room × plan ÷ days², with days floored at 0.5 and 7 when no reset is known.
// Its automatic pick took the best score; here, as in routing, nobody eligible does not
// fail the request, so the first available account takes it.
func simV1Score(_ *simRotation, accounts []*simAccount, now time.Time, _ bool) *simAccount {
	var best *simAccount
	bestScore := -1.0
	for _, account := range accounts {
		var five, week QuotaResetWindow
		for _, window := range account.seen.Windows {
			if window.Seconds == 18000 {
				five = window
			} else {
				week = window
			}
		}
		if five.UsedPercent >= 90 || week.UsedPercent >= 90 {
			continue
		}
		days := 7.0
		if !week.ResetAt.IsZero() {
			days = week.ResetAt.Sub(now).Hours() / 24
		}
		days = math.Max(days, 0.5)
		score := math.Max(0, 1-week.UsedPercent/100) * account.plan / (days * days)
		if score > bestScore {
			best, bestScore = account, score
		}
	}
	if best == nil {
		return accounts[0]
	}
	return best
}

// simRealOrder runs the production ranking on the same readings.
func simRealOrder(policy soonestResetPolicy) func(*simRotation, []*simAccount, time.Time, bool) *simAccount {
	return func(_ *simRotation, accounts []*simAccount, now time.Time, cold bool) *simAccount {
		board := &quotaResetBoard{hints: make(map[string]QuotaResetHint, len(accounts))}
		auths := make([]*Auth, 0, len(accounts))
		byID := make(map[string]*simAccount, len(accounts))
		for _, account := range accounts {
			board.hints[account.id] = account.seen
			auths = append(auths, &Auth{ID: account.id, Provider: "claude", Attributes: map[string]string{"plan_type": fmt.Sprintf("max%.0fx", account.plan)}})
			byID[account.id] = account
		}
		applied := soonestResetPolicy{}
		if cold {
			applied = policy
		}
		return byID[soonestResetOrder(board, auths, "", now, applied)[0].auths[0].ID]
	}
}

type simScenario struct {
	name     string
	accounts func(start time.Time, rng *rand.Rand) []*simAccount
	demand   float64 // share of weekly capacity that sessions ask for
	parallel int     // sessions that start together
	// fiveShare is the 5-hour cap as a share of the weekly cap, and hourly a session's
	// use per hour as a share of an average 5-hour cap.
	fiveShare, hourly float64
	readEvery         time.Duration
}

func simStaggered(plans ...float64) func(time.Time, *rand.Rand) []*simAccount {
	return func(start time.Time, rng *rand.Rand) []*simAccount {
		out := make([]*simAccount, len(plans))
		for i, plan := range plans {
			out[i] = &simAccount{id: fmt.Sprintf("a%d", i), plan: plan}
			out[i].weekUsed = out[i].weekCap() * (0.1 + 0.6*rng.Float64())
			out[i].weekReset = start.Add(simWeek * time.Duration(i+1) / time.Duration(len(plans)))
		}
		return out
	}
}

// simOwner is the owner's Claude accounts on 2026-11-01.
func simOwner(plans [4]float64) func(time.Time, *rand.Rand) []*simAccount {
	return func(start time.Time, _ *rand.Rand) []*simAccount {
		left := [4]float64{44, 40, 87, 0}
		resets := [4]time.Duration{3 * time.Hour, 22 * time.Hour, 6*24*time.Hour + 15*time.Hour, 36 * time.Hour}
		out := make([]*simAccount, 4)
		for i := range out {
			out[i] = &simAccount{id: fmt.Sprintf("a%d", i), plan: plans[i]}
			out[i].weekUsed = out[i].weekCap() * (100 - left[i]) / 100
			out[i].weekReset = start.Add(resets[i])
			if left[i] == 0 {
				out[i].cooled = out[i].weekReset
			}
		}
		return out
	}
}

func simSessions(s simScenario, accounts []*simAccount, start time.Time, rng *rand.Rand) []*simSession {
	capacity := 0.0
	for _, account := range accounts {
		capacity += account.weekCap()
	}
	const hoursPerSession = 3.0
	mean := capacity / float64(len(accounts)) * s.fiveShare * s.hourly
	total := s.demand * capacity * (float64(simHorizon) / float64(simWeek))
	var out []*simSession
	asked := 0.0
	for asked < total {
		// Sessions arrive across a 14-hour working day.
		at := start.Add(time.Duration(rng.Int63n(int64(simHorizon/(24*time.Hour)))) * 24 * time.Hour).
			Add(time.Duration(8*60+rng.Intn(14*60)) * time.Minute)
		for range max(s.parallel, 1) {
			hours := hoursPerSession * (0.4 + 1.2*rng.Float64())
			rate := mean * (0.5 + rng.Float64())
			end := at.Add(time.Duration(hours * float64(time.Hour)))
			out = append(out, &simSession{start: at, end: end, rate: rate})
			asked += hours * rate
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out
}

func simRun(s simScenario, order simOrder, seed int64) simResult {
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(seed))
	accounts := s.accounts(start, rng)
	for _, account := range accounts {
		account.fiveShare = s.fiveShare
		account.read(start)
	}
	sessions := simSessions(s, accounts, start, rng)
	rotation := &simRotation{}
	var result simResult
	available := func(now time.Time, tried map[string]bool) []*simAccount {
		var out []*simAccount
		for _, account := range accounts {
			if !account.cooled.After(now) && !tried[account.id] {
				out = append(out, account)
			}
		}
		return out
	}
	var live []*simSession
	next := 0
	dt := simStep.Hours()
	for now := start; now.Before(start.Add(simHorizon)); now = now.Add(simStep) {
		for _, account := range accounts {
			if !account.weekReset.IsZero() && !account.weekReset.After(now) {
				result.due += account.weekCap()
				result.wasted += math.Max(0, account.weekCap()-account.weekUsed)
				account.weekUsed, account.weekReset = 0, time.Time{}
			}
			if !account.fiveReset.IsZero() && !account.fiveReset.After(now) {
				account.fiveUsed, account.fiveReset = 0, time.Time{}
			}
		}
		if now.Sub(start)%s.readEvery == 0 {
			for _, account := range accounts {
				account.read(now)
			}
		}
		for next < len(sessions) && !sessions[next].start.After(now) {
			live = append(live, sessions[next])
			next++
		}
		kept := live[:0]
		for _, session := range live {
			if !session.end.After(now) {
				continue
			}
			kept = append(kept, session)
			need := session.rate * dt
			result.demand += need
			tried := map[string]bool{}
			for need > 1e-9 {
				if session.bound != nil && session.bound.cooled.After(now) {
					session.bound = nil
					result.failedOver++
				}
				if session.bound == nil {
					choices := available(now, tried)
					if len(choices) == 0 {
						result.unmet += need
						result.failed++
						break
					}
					session.bound = order.pick(rotation, choices, now, len(tried) == 0)
				}
				account := session.bound
				weekRoom := account.weekCap() - account.weekUsed
				fiveRoom := account.fiveCap() - account.fiveUsed
				take := math.Min(need, math.Min(weekRoom, fiveRoom))
				if take > 0 {
					if account.weekReset.IsZero() {
						account.weekReset = now.Add(simWeek)
					}
					if account.fiveReset.IsZero() {
						account.fiveReset = now.Add(5 * time.Hour)
					}
					account.weekUsed += take
					account.fiveUsed += take
					need -= take
				}
				if need <= 1e-9 {
					break
				}
				if fiveRoom <= weekRoom {
					result.sessionCaps++
					account.cooled = account.fiveReset
				} else {
					result.weeklyCaps++
					account.cooled = account.weekReset
				}
				tried[account.id] = true
				session.bound = nil
				result.failedOver++
			}
		}
		live = kept
	}
	return result
}

func simOrders() []simOrder {
	return []simOrder{
		{"round-robin", simRoundRobin},
		{"v1-score", simV1Score},
		{"soonest-reset", simRealOrder(soonestResetPolicy{})},
		{"edf-90/3", simRealOrder(soonestResetPolicy{floor: 3, sessionCeiling: 90})},
		{"edf-pace", simRealOrder(soonestResetGates)},
	}
}

func simScenarios(fiveShare, hourly float64, readEvery time.Duration) []simScenario {
	equal := simStaggered(5, 5, 5, 5)
	mixed := simStaggered(20, 5, 5, 1)
	var out []simScenario
	for _, demand := range []float64{0.5, 0.8, 1.1} {
		for _, s := range []simScenario{
			{name: "equal", accounts: equal, parallel: 1},
			{name: "plans", accounts: mixed, parallel: 1},
			{name: "fan-out", accounts: equal, parallel: 6},
			{name: "owner", accounts: simOwner([4]float64{5, 5, 5, 5}), parallel: 2},
			{name: "owner plans", accounts: simOwner([4]float64{5, 20, 5, 1}), parallel: 2},
		} {
			s.name = fmt.Sprintf("%s %.0f%%", s.name, demand*100)
			s.demand, s.fiveShare, s.hourly, s.readEvery = demand, fiveShare, hourly, readEvery
			out = append(out, s)
		}
	}
	return out
}

// simSum runs each scenario once per seed, in parallel, and adds results in a fixed order.
func simSum(scenarios []simScenario, order simOrder) []simResult {
	results := make([]simResult, len(scenarios))
	var runs sync.WaitGroup
	for i, scenario := range scenarios {
		runs.Go(func() {
			for seed := int64(1); seed <= simSeeds; seed++ {
				results[i].add(simRun(scenario, order, seed))
			}
		})
	}
	runs.Wait()
	return results
}

func (r simResult) cells() string {
	return fmt.Sprintf("%8.1f %7.1f %9.1f %8.1f %7.1f %9.1f", 100*r.wasted/math.Max(r.due, 1), 100*r.unmet/math.Max(r.demand, 1), r.failedOver/simSeeds, r.sessionCaps/simSeeds, r.weeklyCaps/simSeeds, r.failed/simSeeds)
}

const simHeader = "\n%-17s %-14s %8s %7s %9s %8s %7s %9s\n"

// TestDynaSoonestResetSimulation is the comparison behind the shipped order. Run with -v
// for the table. It fails if a change makes another order win on what the order is for.
func TestDynaSoonestResetSimulation(t *testing.T) {
	scenarios := simScenarios(0.125, 0.11, SoonestResetRefreshInterval)
	var table strings.Builder
	fmt.Fprintf(&table, simHeader, "scenario", "order", "wasted%", "unmet%", "failover", "5h-caps", "wk-caps", "failed")
	totals := make(map[string]simResult)
	byOrder := make(map[string][]simResult)
	for _, order := range simOrders() {
		byOrder[order.name] = simSum(scenarios, order)
	}
	for i, scenario := range scenarios {
		for _, order := range simOrders() {
			result := byOrder[order.name][i]
			total := totals[order.name]
			total.add(result)
			totals[order.name] = total
			fmt.Fprintf(&table, "%-17s %-14s %s\n", scenario.name, order.name, result.cells())
		}
	}
	for _, order := range simOrders() {
		fmt.Fprintf(&table, "%-17s %-14s %s\n", "ALL", order.name, totals[order.name].cells())
	}
	t.Log(table.String())
	shipped, v1, robin, spec := totals["edf-pace"], totals["v1-score"], totals["round-robin"], totals["edf-90/3"]
	waste := func(r simResult) float64 { return r.wasted / r.due }
	if waste(shipped) >= waste(v1) || waste(shipped) >= waste(robin) {
		t.Errorf("edf-pace wastes more: %s, v1 %s, round-robin %s", shipped.cells(), v1.cells(), robin.cells())
	}
	if shipped.failedOver >= v1.failedOver || shipped.sessionCaps >= v1.sessionCaps {
		t.Errorf("edf-pace disrupts more than v1: %s, v1 %s", shipped.cells(), v1.cells())
	}
	if shipped.failedOver >= spec.failedOver || shipped.sessionCaps >= spec.sessionCaps || shipped.unmet >= spec.unmet || waste(shipped)-waste(spec) > 0.002 {
		t.Errorf("the pace rule and reserve no longer pay: %s, plain gates %s", shipped.cells(), spec.cells())
	}
}

// TestDynaSoonestResetSimulationCalibrations guards against choosing an order that only
// wins on one guess at the 5-hour cap and session size.
func TestDynaSoonestResetSimulationCalibrations(t *testing.T) {
	if testing.Short() {
		t.Skip("runs 1620 simulations per order")
	}
	var table strings.Builder
	fmt.Fprintf(&table, simHeader, "calibrations", "order", "wasted%", "unmet%", "failover", "5h-caps", "wk-caps", "failed")
	totals := make(map[string]simResult)
	for _, order := range simOrders() {
		var total simResult
		for _, fiveShare := range []float64{0.08, 0.125, 0.2} {
			for _, hourly := range []float64{0.07, 0.11, 0.18} {
				for _, result := range simSum(simScenarios(fiveShare, hourly, SoonestResetRefreshInterval), order) {
					total.add(result)
				}
			}
		}
		totals[order.name] = total
		fmt.Fprintf(&table, "%-17s %-14s %s\n", "all 9", order.name, total.cells())
	}
	t.Log(table.String())
	shipped, v1 := totals["edf-pace"], totals["v1-score"]
	if shipped.wasted/shipped.due >= v1.wasted/v1.due || shipped.failedOver >= v1.failedOver || shipped.sessionCaps >= v1.sessionCaps {
		t.Errorf("edf-pace loses to v1 across calibrations: %s, v1 %s", shipped.cells(), v1.cells())
	}
}

// TestDynaSoonestResetSimulationReadings reports what fresher readings would buy, which is
// the evidence for SoonestResetRefreshInterval.
func TestDynaSoonestResetSimulationReadings(t *testing.T) {
	var table strings.Builder
	fmt.Fprintf(&table, simHeader, "read every", "order", "wasted%", "unmet%", "failover", "5h-caps", "wk-caps", "failed")
	for _, every := range []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, 60 * time.Minute} {
		var total simResult
		for _, result := range simSum(simScenarios(0.125, 0.11, every), simOrder{"edf-pace", simRealOrder(soonestResetGates)}) {
			total.add(result)
		}
		fmt.Fprintf(&table, "%-17s %-14s %s\n", every, "edf-pace", total.cells())
	}
	t.Log(table.String())
}
