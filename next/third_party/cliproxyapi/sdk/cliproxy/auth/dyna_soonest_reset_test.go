package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

var resetNow = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func resetWeek(left float64, in time.Duration) QuotaResetWindow {
	return QuotaResetWindow{Seconds: 604800, UsedPercent: 100 - left, ResetAt: resetNow.Add(in)}
}

func resetFive(used float64, in time.Duration) QuotaResetWindow {
	return QuotaResetWindow{Seconds: 18000, UsedPercent: used, ResetAt: resetNow.Add(in)}
}

func resetUnstartedWeek() QuotaResetWindow { return QuotaResetWindow{Seconds: 604800} }

func resetBoard(readings map[string][]QuotaResetWindow) *quotaResetBoard {
	board := &quotaResetBoard{hints: make(map[string]QuotaResetHint)}
	for id, windows := range readings {
		board.hints[id] = QuotaResetHint{ObservedAt: resetNow.Add(-time.Minute), Windows: windows}
	}
	return board
}

func resetAuths(ids ...string) []*Auth {
	auths := make([]*Auth, 0, len(ids))
	for _, id := range ids {
		auths = append(auths, &Auth{ID: id, Provider: "claude", Metadata: map[string]any{"type": "claude"}})
	}
	return auths
}

func resetPick(t *testing.T, board *quotaResetBoard, auths []*Auth, model string) string {
	t.Helper()
	selector := &SoonestResetSelector{now: func() time.Time { return resetNow }}
	ctx := context.WithValue(context.Background(), soonestResetBoardKey{}, board)
	auth, err := selector.Pick(ctx, "claude", model, cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatal(err)
	}
	return auth.ID
}

func resetOrderIDs(board *quotaResetBoard, auths []*Auth, model string, policy soonestResetPolicy) [][]string {
	var out [][]string
	for _, place := range soonestResetOrder(board, auths, model, resetNow, policy) {
		var ids []string
		for _, auth := range place.auths {
			ids = append(ids, auth.ID)
		}
		out = append(out, ids)
	}
	return out
}

func TestSoonestResetOwnerExample(t *testing.T) {
	board := resetBoard(map[string][]QuotaResetWindow{
		"pipsqueak":     {resetFive(10, 2*time.Hour), resetWeek(44, 3*time.Hour)},
		"aria":          {resetFive(10, 4*time.Hour), resetWeek(40, 22*time.Hour)},
		"figueredoaria": {resetFive(0, 0), resetWeek(87, 6*24*time.Hour+15*time.Hour)},
		"spent":         {resetFive(0, 0), resetWeek(0, 36*time.Hour)},
	})
	got := resetOrderIDs(board, resetAuths("spent", "figueredoaria", "aria", "pipsqueak"), "", soonestResetGates)
	want := []string{"pipsqueak", "aria", "figueredoaria", "spent"}
	if len(got) != len(want) {
		t.Fatalf("order = %v", got)
	}
	for i, id := range want {
		if len(got[i]) != 1 || got[i][0] != id {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestSoonestResetUsesLongWindowNotFiveHour(t *testing.T) {
	board := resetBoard(map[string][]QuotaResetWindow{
		"soon-five": {resetFive(5, 10*time.Minute), resetWeek(50, 48*time.Hour)},
		"soon-week": {resetFive(5, 4*time.Hour), resetWeek(50, 12*time.Hour)},
	})
	if got := resetPick(t, board, resetAuths("soon-five", "soon-week"), ""); got != "soon-week" {
		t.Fatalf("picked %s", got)
	}
}

func TestSoonestResetDeadlineIsWindowWithLeastLeft(t *testing.T) {
	scoped := resetWeek(5, 48*time.Hour)
	scoped.Model = "Fable"
	reading := []QuotaResetWindow{resetWeek(60, 100*time.Hour), scoped}
	board := resetBoard(map[string][]QuotaResetWindow{"tight": reading, "other": {resetWeek(60, 72*time.Hour)}})
	auths := resetAuths("tight", "other")
	if got := resetOrderIDs(board, auths, "claude-fable-5", soonestResetPolicy{}); got[0][0] != "tight" {
		t.Fatalf("a matching window should set the deadline: %v", got)
	}
	if got := resetOrderIDs(board, auths, "claude-opus-5", soonestResetPolicy{}); got[0][0] != "other" {
		t.Fatalf("a window for another model should not apply: %v", got)
	}
}

func TestSoonestResetSpentModelWindowOnlyBlocksThatModel(t *testing.T) {
	spent := resetWeek(0, 96*time.Hour)
	spent.Model = "Fable"
	board := resetBoard(map[string][]QuotaResetWindow{
		"fable-gone": {resetWeek(60, 24*time.Hour), spent},
		"later":      {resetWeek(60, 72*time.Hour)},
	})
	auths := resetAuths("fable-gone", "later")
	if got := resetPick(t, board, auths, "claude-fable-5"); got != "later" {
		t.Fatalf("fable picked %s", got)
	}
	if got := resetPick(t, board, auths, "claude-opus-5"); got != "fable-gone" {
		t.Fatalf("opus picked %s", got)
	}
}

func TestSoonestResetClasses(t *testing.T) {
	key := &Auth{ID: "key", Provider: "claude", Attributes: map[string]string{"api_key": "invented"}}
	prepaid := &Auth{ID: "prepaid", Provider: "claude", Metadata: map[string]any{"type": "claude"}}
	board := resetBoard(map[string][]QuotaResetWindow{
		"running":   {resetWeek(50, 96*time.Hour)},
		"unstarted": {resetFive(5, time.Hour), resetUnstartedWeek()},
		"spent":     {resetWeek(0, time.Hour)},
	})
	board.hints["prepaid"] = QuotaResetHint{ObservedAt: resetNow, Prepaid: true}
	auths := append(resetAuths("spent", "unknown", "unstarted", "running"), key, prepaid)
	got := resetOrderIDs(board, auths, "", soonestResetGates)
	want := [][]string{{"running"}, {"unstarted"}, {"unknown"}, {"key", "prepaid"}, {"spent"}}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) || got[i][0] != want[i][0] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestSoonestResetGates(t *testing.T) {
	tests := []struct {
		name    string
		reading []QuotaResetWindow
	}{
		{"long floor", []QuotaResetWindow{resetWeek(SoonestResetLongFloorPercent, 2*time.Hour)}},
		{"reserve on a far reset", []QuotaResetWindow{resetWeek(SoonestResetLongReservePercent, 48*time.Hour)}},
		{"session ceiling", []QuotaResetWindow{resetFive(SoonestResetSessionCeilingPercent, time.Hour), resetWeek(50, 2*time.Hour)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			board := resetBoard(map[string][]QuotaResetWindow{"soon": test.reading, "later": {resetWeek(50, 120*time.Hour)}})
			auths := resetAuths("soon", "later")
			if got := resetPick(t, board, auths, ""); got != "later" {
				t.Fatalf("cold pick took %s", got)
			}
			if got := resetOrderIDs(board, auths, "", soonestResetPolicy{}); got[0][0] != "soon" {
				t.Fatalf("failover should ignore the gate: %v", got)
			}
			if got := resetPick(t, board, resetAuths("soon"), ""); got != "soon" {
				t.Fatalf("with nobody else the gate should drop, got %s", got)
			}
		})
	}
}

func TestSoonestResetReserveLiftsNearTheDeadline(t *testing.T) {
	board := resetBoard(map[string][]QuotaResetWindow{"soon": {resetWeek(8, 20*time.Hour)}, "later": {resetWeek(50, 120*time.Hour)}})
	if got := resetPick(t, board, resetAuths("soon", "later"), ""); got != "soon" {
		t.Fatalf("picked %s", got)
	}
}

func TestSoonestResetPaceSortsAfterRunningBeforeUnstarted(t *testing.T) {
	// Half the cap used an hour into the window is on course for far more than the ceiling.
	paced := resetFive(50, 4*time.Hour)
	board := resetBoard(map[string][]QuotaResetWindow{
		"paced":     {paced, resetWeek(50, 2*time.Hour)},
		"calm":      {resetFive(5, 4*time.Hour), resetWeek(50, 96*time.Hour)},
		"unstarted": {resetUnstartedWeek()},
	})
	got := resetOrderIDs(board, resetAuths("paced", "calm", "unstarted"), "", soonestResetGates)
	if got[0][0] != "calm" || got[1][0] != "paced" || got[2][0] != "unstarted" {
		t.Fatalf("order = %v", got)
	}
}

func TestSoonestResetEqualDeadlinesPreferMoreQuotaThenID(t *testing.T) {
	big := &Auth{ID: "z-big", Provider: "claude", Metadata: map[string]any{"type": "claude"}, Attributes: map[string]string{"plan_type": "max20x"}}
	small := &Auth{ID: "a-small", Provider: "claude", Metadata: map[string]any{"type": "claude"}, Attributes: map[string]string{"plan_type": "max_5x"}}
	board := resetBoard(map[string][]QuotaResetWindow{
		"z-big":   {resetWeek(30, 24*time.Hour)},
		"a-small": {resetWeek(40, 24*time.Hour)},
		"b-same":  {resetWeek(30, 24*time.Hour)},
		"c-same":  {resetWeek(30, 24*time.Hour)},
	})
	if got := resetOrderIDs(board, []*Auth{small, big}, "", soonestResetGates); got[0][0] != "z-big" {
		t.Fatalf("plan size should decide: %v", got)
	}
	if got := resetPick(t, board, resetAuths("c-same", "b-same"), ""); got != "b-same" {
		t.Fatalf("picked %s", got)
	}
}

func TestSoonestResetStaleFallsBackToRoundRobin(t *testing.T) {
	passed := resetWeek(50, -time.Minute)
	tests := map[string]*quotaResetBoard{
		"missing":      nil,
		"reset passed": resetBoard(map[string][]QuotaResetWindow{"a": {passed}, "b": {passed}, "c": {passed}}),
		"too old": {hints: map[string]QuotaResetHint{
			"a": {ObservedAt: resetNow.Add(-soonestResetMaxAge), Windows: []QuotaResetWindow{resetWeek(50, time.Hour)}},
			"b": {ObservedAt: resetNow.Add(-soonestResetMaxAge), Windows: []QuotaResetWindow{resetWeek(50, 2*time.Hour)}},
			"c": {ObservedAt: resetNow.Add(-soonestResetMaxAge), Windows: []QuotaResetWindow{resetWeek(50, 3*time.Hour)}},
		}},
	}
	for name, board := range tests {
		t.Run(name, func(t *testing.T) {
			selector := &SoonestResetSelector{now: func() time.Time { return resetNow }}
			ctx := context.WithValue(context.Background(), soonestResetBoardKey{}, board)
			seen := make(map[string]int)
			for range 9 {
				auth, err := selector.Pick(ctx, "claude", "", cliproxyexecutor.Options{}, resetAuths("c", "a", "b"))
				if err != nil {
					t.Fatal(err)
				}
				seen[auth.ID]++
			}
			if seen["a"] != 3 || seen["b"] != 3 || seen["c"] != 3 {
				t.Fatalf("picks = %v", seen)
			}
		})
	}
}

func TestSoonestResetPassedFiveHourDoesNotEndTheReading(t *testing.T) {
	board := resetBoard(map[string][]QuotaResetWindow{
		"later": {resetFive(100, -time.Minute), resetWeek(50, 48*time.Hour)},
		"none":  nil,
	})
	got := resetOrderIDs(board, resetAuths("later", "none"), "", soonestResetGates)
	if got[0][0] != "later" {
		t.Fatalf("order = %v", got)
	}
}

func TestSoonestResetDropsReadingOfReregisteredCredential(t *testing.T) {
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	auth := &Auth{ID: "a", Provider: "claude", RegistrationEpoch: 1}
	manager.mu.Lock()
	manager.auths["a"] = auth
	manager.mu.Unlock()
	manager.PublishQuotaReading("a", QuotaResetHint{ObservedAt: resetNow, Windows: []QuotaResetWindow{resetWeek(50, time.Hour)}})
	if _, ok := manager.quotaBoard.hint(auth); !ok {
		t.Fatal("reading was not kept")
	}
	if _, ok := manager.quotaBoard.hint(&Auth{ID: "a", RegistrationEpoch: 2}); ok {
		t.Fatal("a new registration inherited the reading")
	}
}

func TestPublishQuotaReadingKeepsTheNewer(t *testing.T) {
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	auth := &Auth{ID: "a", Provider: "claude"}
	manager.mu.Lock()
	manager.auths["a"] = auth
	manager.mu.Unlock()
	manager.PublishQuotaReading("a", QuotaResetHint{ObservedAt: resetNow, Prepaid: true})
	manager.PublishQuotaReading("a", QuotaResetHint{ObservedAt: resetNow.Add(-time.Hour)})
	if hint, _ := manager.quotaBoard.hint(auth); !hint.Prepaid {
		t.Fatal("an older reading replaced a newer one")
	}
}

// heldUsageRead counts refreshes and holds each one until released.
type heldUsageRead struct {
	calls   atomic.Int32
	release chan struct{}
}

func (b *heldUsageRead) run(string) bool {
	b.calls.Add(1)
	<-b.release
	return false
}

func waitBoardIdle(t *testing.T, board *quotaResetBoard, provider string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		board.mu.RLock()
		running := board.running[provider]
		board.mu.RUnlock()
		if !running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("refresh did not finish")
}

func TestPickNeverWaitsForARefresh(t *testing.T) {
	refresh := &heldUsageRead{release: make(chan struct{})}
	board := &quotaResetBoard{refresh: refresh.run}
	done := make(chan string, 1)
	go func() {
		selector := &SoonestResetSelector{now: func() time.Time { return resetNow }}
		ctx := context.WithValue(context.Background(), soonestResetBoardKey{}, board)
		auth, err := selector.Pick(ctx, "claude", "", cliproxyexecutor.Options{}, resetAuths("a", "b"))
		if err != nil {
			done <- err.Error()
			return
		}
		done <- auth.ID
	}()
	select {
	case got := <-done:
		if got != "a" && got != "b" {
			t.Fatalf("pick = %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pick waited for a usage read")
	}
	close(refresh.release)
	waitBoardIdle(t, board, "claude")
}

func TestRefreshIsDedupedAndRateLimited(t *testing.T) {
	refresh := &heldUsageRead{release: make(chan struct{})}
	board := &quotaResetBoard{refresh: refresh.run}
	auths := resetAuths("a", "b")
	var picks sync.WaitGroup
	for range 64 {
		picks.Go(func() { board.requestRefresh(auths, resetNow) })
	}
	picks.Wait()
	close(refresh.release)
	waitBoardIdle(t, board, "claude")
	if got := refresh.calls.Load(); got != 1 {
		t.Fatalf("refreshes = %d", got)
	}
	board.requestRefresh(auths, resetNow.Add(SoonestResetRefreshInterval-time.Second))
	waitBoardIdle(t, board, "claude")
	if got := refresh.calls.Load(); got != 1 {
		t.Fatalf("refresh inside the interval: %d", got)
	}
	board.requestRefresh(auths, resetNow.Add(SoonestResetRefreshInterval))
	waitBoardIdle(t, board, "claude")
	if got := refresh.calls.Load(); got != 2 {
		t.Fatalf("refresh after the interval: %d", got)
	}
}

func TestRefreshBacksOffAfterARateLimit(t *testing.T) {
	var calls atomic.Int32
	board := &quotaResetBoard{refresh: func(string) bool { calls.Add(1); return true }}
	auths := resetAuths("a")
	at := resetNow
	for want, wait := range []time.Duration{20 * time.Minute, 40 * time.Minute, 80 * time.Minute, soonestResetRefreshCeiling, soonestResetRefreshCeiling} {
		board.requestRefresh(auths, at)
		waitBoardIdle(t, board, "claude")
		if got := int(calls.Load()); got != want+1 {
			t.Fatalf("attempt %d: calls = %d", want, got)
		}
		board.requestRefresh(auths, at.Add(wait-time.Second))
		waitBoardIdle(t, board, "claude")
		if got := int(calls.Load()); got != want+1 {
			t.Fatalf("attempt %d read inside its %s backoff", want, wait)
		}
		at = at.Add(wait)
	}
}

func TestFreshReadingsAskForNoRefresh(t *testing.T) {
	var calls atomic.Int32
	board := resetBoard(map[string][]QuotaResetWindow{"a": {resetWeek(50, time.Hour)}})
	board.refresh = func(string) bool { calls.Add(1); return false }
	board.requestRefresh(resetAuths("a"), resetNow)
	waitBoardIdle(t, board, "claude")
	if calls.Load() != 0 {
		t.Fatal("a current reading started a refresh")
	}
}

func TestPassedResetAsksForARefresh(t *testing.T) {
	var calls atomic.Int32
	board := resetBoard(map[string][]QuotaResetWindow{"a": {resetWeek(50, -time.Minute)}})
	board.refresh = func(string) bool { calls.Add(1); return false }
	board.requestRefresh(resetAuths("a"), resetNow)
	waitBoardIdle(t, board, "claude")
	if calls.Load() != 1 {
		t.Fatal("a passed reset did not start a refresh")
	}
}

func TestRefreshPanicDoesNotStopLaterRefreshes(t *testing.T) {
	var calls atomic.Int32
	board := &quotaResetBoard{refresh: func(string) bool {
		calls.Add(1)
		panic("invented")
	}}
	auths := resetAuths("a")
	board.requestRefresh(auths, resetNow)
	waitBoardIdle(t, board, "claude")
	board.requestRefresh(auths, resetNow.Add(SoonestResetRefreshInterval))
	waitBoardIdle(t, board, "claude")
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

type resetExecutor struct{ schedulerTestExecutor }

func (resetExecutor) Identifier() string { return "claude" }

// resetManager serves real selection through a Manager with session affinity.
func resetManager(t *testing.T, ids ...string) *Manager {
	t.Helper()
	selector := NewSessionAffinitySelector(&SoonestResetSelector{})
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(resetExecutor{})
	for _, id := range ids {
		if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: "claude", Metadata: map[string]any{"type": "claude"}}); err != nil {
			t.Fatal(err)
		}
	}
	return manager
}

func resetSessionOptions(session string) cliproxyexecutor.Options {
	headers := make(http.Header)
	headers.Set("X-Claude-Code-Session-Id", session)
	return cliproxyexecutor.Options{Headers: headers}
}

func resetPublish(manager *Manager, id string, windows ...QuotaResetWindow) {
	manager.PublishQuotaReading(id, QuotaResetHint{ObservedAt: time.Now().Add(-time.Second), Windows: windows})
}

func resetFutureWeek(left float64, in time.Duration) QuotaResetWindow {
	return QuotaResetWindow{Seconds: 604800, UsedPercent: 100 - left, ResetAt: time.Now().Add(in)}
}

func TestSessionStaysBoundWhenRankChanges(t *testing.T) {
	manager := resetManager(t, "first", "second")
	resetPublish(manager, "first", resetFutureWeek(50, 2*time.Hour))
	resetPublish(manager, "second", resetFutureWeek(50, 48*time.Hour))
	ctx := context.Background()
	picked, _, err := manager.pickNext(ctx, "claude", "", resetSessionOptions("session-1"), nil)
	if err != nil || picked.ID != "first" {
		t.Fatalf("cold pick = %v, %v", picked, err)
	}
	resetPublish(manager, "second", resetFutureWeek(50, time.Hour))
	picked, _, err = manager.pickNext(ctx, "claude", "", resetSessionOptions("session-1"), nil)
	if err != nil || picked.ID != "first" {
		t.Fatalf("bound session moved to %v, %v", picked, err)
	}
	other, _, err := manager.pickNext(ctx, "claude", "", resetSessionOptions("session-2"), nil)
	if err != nil || other.ID != "second" {
		t.Fatalf("a new session should take the new soonest: %v, %v", other, err)
	}
}

func TestFailoverRebindsToNextSoonestPastTheGate(t *testing.T) {
	manager := resetManager(t, "first", "second", "third")
	resetPublish(manager, "first", resetFutureWeek(50, 2*time.Hour))
	// second's reset comes next, but its few percent left gate it for new sessions.
	resetPublish(manager, "second", resetFutureWeek(2, 12*time.Hour))
	resetPublish(manager, "third", resetFutureWeek(50, 96*time.Hour))
	ctx := context.Background()
	picked, _, err := manager.pickNext(ctx, "claude", "", resetSessionOptions("session-1"), nil)
	if err != nil || picked.ID != "first" {
		t.Fatalf("cold pick = %v, %v", picked, err)
	}
	picked, _, err = manager.pickNext(ctx, "claude", "", resetSessionOptions("session-1"), map[string]struct{}{"first": {}})
	if err != nil || picked.ID != "second" {
		t.Fatalf("failover took %v, %v, want the next soonest", picked, err)
	}
	picked, _, err = manager.pickNext(ctx, "claude", "", resetSessionOptions("session-2"), nil)
	if err != nil || picked.ID != "first" {
		t.Fatalf("cold pick after failover = %v, %v", picked, err)
	}
	cold, _, err := manager.pickNext(ctx, "claude", "", resetSessionOptions("session-3"), map[string]struct{}{"first": {}, "second": {}})
	if err != nil || cold.ID != "third" {
		t.Fatalf("failover past two = %v, %v", cold, err)
	}
}

func TestRoutingStatusReportsTheSelectorsOrder(t *testing.T) {
	manager := resetManager(t, "first", "second", "third", "spent")
	resetPublish(manager, "first", resetFutureWeek(50, 2*time.Hour))
	resetPublish(manager, "second", resetFutureWeek(50, 12*time.Hour))
	resetPublish(manager, "third", resetFutureWeek(50, 96*time.Hour))
	resetPublish(manager, "spent", resetFutureWeek(0, time.Hour))
	views := manager.QuotaRoutingSnapshot().Providers["claude"]
	if len(views) != 4 {
		t.Fatalf("views = %+v", views)
	}
	picked, _, err := manager.pickNext(context.Background(), "claude", "", resetSessionOptions("session-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if views[0].Rank != 1 || views[0].AuthIndex != picked.EnsureIndex() {
		t.Fatalf("status said %+v, routing picked %s", views[0], picked.EnsureIndex())
	}
	if last := views[3]; !last.Spent || last.Rank != 4 {
		t.Fatalf("last = %+v", last)
	}
}

func TestRoutingStatusAllSpentHasNoUsableRankOne(t *testing.T) {
	manager := resetManager(t, "a", "b")
	resetPublish(manager, "a", resetFutureWeek(0, time.Hour))
	resetPublish(manager, "b", resetFutureWeek(0, 2*time.Hour))
	for _, view := range manager.QuotaRoutingSnapshot().Providers["claude"] {
		if !view.Spent {
			t.Fatalf("view = %+v", view)
		}
	}
}

func TestSetSelectorKeepsReadings(t *testing.T) {
	manager := resetManager(t, "a")
	resetPublish(manager, "a", resetFutureWeek(50, 2*time.Hour))
	manager.SetSelector(NewSessionAffinitySelector(&SoonestResetSelector{}))
	view := manager.QuotaRoutingSnapshot().Providers["claude"]
	if len(view) != 1 || view[0].Deadline == nil {
		t.Fatalf("views = %+v", view)
	}
}

func TestPickSpreadsAndSkipsZeroWeight(t *testing.T) {
	heavy := &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeWeight: "0"}}
	board := resetBoard(nil)
	if got := resetPick(t, board, []*Auth{heavy, resetAuths("b")[0]}, ""); got != "b" {
		t.Fatalf("picked %s", got)
	}
}

func TestPickToleratesANilContext(t *testing.T) {
	selector := &SoonestResetSelector{now: func() time.Time { return resetNow }}
	//nolint:staticcheck // Upstream's selectors accept a nil context, so this one must.
	if auth, err := selector.Pick(nil, "claude", "", cliproxyexecutor.Options{}, resetAuths("a")); err != nil || auth.ID != "a" {
		t.Fatalf("pick = %v, %v", auth, err)
	}
	var manager Manager
	//nolint:staticcheck // As above.
	if manager.withSoonestReset(nil, nil) == nil || withSoonestResetFailover(nil) == nil {
		t.Fatal("no context")
	}
}
