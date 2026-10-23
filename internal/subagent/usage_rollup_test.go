package subagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/workeroutput"
)

var rollupDay = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func rollupStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { _ = database.Close(dir) })
	return &Store{Root: filepath.Join(dir, "sessions")}
}

func seedRollupSession(t *testing.T, s *Store, id, roomID, role string, turns ...Turn) Meta {
	t.Helper()
	dir, err := s.Dir(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: id, Name: id, RoomID: roomID, Room: roomID, Role: role, Profile: "p", Turns: len(turns)}
	if err := s.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	for i := range turns {
		turns[i].N = i + 1
		// Usage fixtures do not model a room log. Give terminal turns a known boundary.
		if turns[i].InputCutoff == nil {
			turns[i].InputCutoff = new(int64)
		}
		if err := s.writeTurn(id, turns[i]); err != nil {
			t.Fatal(err)
		}
	}
	return meta
}

func knownTurn(in, out int64, cost float64) Turn {
	return Turn{Status: TurnOK, Harness: "claude-code", Profile: "p", Model: "m", Started: rollupDay, Ended: rollupDay.Add(2 * time.Second),
		Usage:              workeroutput.Usage{InputTokens: in, OutputTokens: out, CostUSD: cost},
		UsageNormalization: workeroutput.UsageNormalization{Version: 1, Input: "complete", CachedInput: "unknown", Output: "complete", Cost: "complete"}}
}

func readAll(t *testing.T, s *Store) UsageRollupSnapshot {
	t.Helper()
	snap, err := s.ReadUsageRollup("", "2026-10-01", "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestRecordTurnUsageIsIdempotent(t *testing.T) {
	s := rollupStore(t)
	turn := knownTurn(100, 40, 0.5)
	turn.N = 1
	meta := Meta{ID: "ses_20261019-120000-aaaaaaaa", RoomID: "r1"}
	for range 3 {
		if err := s.RecordTurnUsage(meta, turn); err != nil {
			t.Fatal(err)
		}
	}
	got := readAll(t, s).Totals
	if got.Turns != 1 || got.WallMs != 2000 || *got.Input.Tokens != 100 || *got.Output.Tokens != 40 || *got.Cost.USD != 0.5 {
		t.Fatalf("totals after replay = %+v", got)
	}
}

func TestUnknownUsageStaysUnknown(t *testing.T) {
	s := rollupStore(t)
	meta := Meta{ID: "ses_20261019-120000-aaaaaaaa", RoomID: "r1"}
	// A legacy turn has no normalization; its stored zeros must not read as 0 tokens.
	legacy := Turn{N: 1, Status: TurnOK, Profile: "p", Model: "m", Started: rollupDay, Ended: rollupDay}
	if err := s.RecordTurnUsage(meta, legacy); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, s).Totals
	if got.Turns != 1 || got.Input.Tokens != nil || got.Cost.USD != nil || got.Input.UnknownTurns != 1 || got.Cost.UnknownTurns != 1 {
		t.Fatalf("legacy turn = %+v", got)
	}
	known := knownTurn(10, 5, 0)
	known.N = 2
	if err := s.RecordTurnUsage(meta, known); err != nil {
		t.Fatal(err)
	}
	got = readAll(t, s).Totals
	if *got.Input.Tokens != 10 || got.Input.CompleteTurns != 1 || got.Input.UnknownTurns != 1 || got.Cached.Tokens != nil || got.Cached.UnknownTurns != 2 {
		t.Fatalf("mixed turns = %+v", got)
	}
}

func TestReconcileRepairsMissedWrite(t *testing.T) {
	s := rollupStore(t)
	id := "ses_20261019-120000-aaaaaaaa"
	meta := seedRollupSession(t, s, id, "r1", "", knownTurn(1, 1, 0), knownTurn(2, 2, 0))
	// The runner crashed between UpdateTurn and the rollup write for turn 2 only.
	first, _ := s.ReadTurn(id, 1)
	if err := s.RecordTurnUsage(meta, first); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s).Totals.Turns; got != 1 {
		t.Fatalf("before repair turns = %d", got)
	}
	for range 2 {
		if err := s.ReconcileUsage(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got := readAll(t, s).Totals
	if got.Turns != 2 || *got.Input.Tokens != 3 {
		t.Fatalf("after repair = %+v", got)
	}
}

func TestReconcileSkipsSessionsAlreadyFolded(t *testing.T) {
	s := rollupStore(t)
	id := "ses_20261019-120000-aaaaaaaa"
	meta := seedRollupSession(t, s, id, "r1", "", knownTurn(1, 1, 0))
	turn, _ := s.ReadTurn(id, 1)
	if err := s.RecordTurnUsage(meta, turn); err != nil {
		t.Fatal(err)
	}
	// An unreadable turn file proves the pass never opens it.
	path, _ := s.turnPath(id, 1)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillFoldsOldTurnsAsUnknown(t *testing.T) {
	s := rollupStore(t)
	old := Turn{Status: TurnOK, Profile: "p", Model: "m", Started: rollupDay, Ended: rollupDay.Add(time.Second)}
	seedRollupSession(t, s, "ses_20261019-120000-aaaaaaaa", "r1", RoleRoot, old, old, knownTurn(7, 3, 0))
	seedRollupSession(t, s, "ses_20261019-120000-bbbbbbbb", "r2", "", old)
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := readAll(t, s)
	if snap.Totals.Turns != 4 || *snap.Totals.Input.Tokens != 7 || snap.Totals.Input.UnknownTurns != 3 {
		t.Fatalf("totals = %+v", snap.Totals)
	}
	room1, err := s.ReadUsageRollup("r1", "2026-10-19", "2026-10-19")
	if err != nil || room1.Totals.Turns != 3 {
		t.Fatalf("room filter = %+v, %v", room1.Totals, err)
	}
}

func TestReadUsageRollupGroups(t *testing.T) {
	s := rollupStore(t)
	other := knownTurn(5, 5, 1)
	other.Model = "n"
	other.Started = rollupDay.AddDate(0, 0, 1)
	seedRollupSession(t, s, "ses_20261019-120000-aaaaaaaa", "r1", RoleRoot, knownTurn(10, 1, 0), other)
	seedRollupSession(t, s, "ses_20261019-120000-bbbbbbbb", "r1", "", knownTurn(20, 2, 0))
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := readAll(t, s)
	if len(snap.ByDayModel) != 2 || snap.ByDayModel[0].Day != "2026-10-19" || *snap.ByDayModel[0].Input.Tokens != 30 || snap.ByDayModel[1].Model != "n" {
		t.Fatalf("byDayModel = %+v", snap.ByDayModel)
	}
	if len(snap.ByRoleModel) != 3 || snap.ByRoleModel[0].Role != RoleRoot || snap.ByRoleModel[2].Role != RoleSubagent {
		t.Fatalf("byRoleModel = %+v", snap.ByRoleModel)
	}
}

func TestRunTurnRecordsUsage(t *testing.T) {
	e := newTestEnv(t)
	t.Cleanup(func() { _ = database.Close(filepath.Dir(e.m.Store.Root)) })
	info, _ := e.start("RESPOND: hello")
	e.wait(info.ID, 1)
	snap, err := e.m.Store.ReadUsageRollup("", "2000-01-01", "2999-12-31")
	if err != nil || snap.Totals.Turns != 1 {
		t.Fatalf("rollup after turn = %+v, %v", snap.Totals, err)
	}
}

func claudeTurn(basis string, cost float64) Turn {
	t := knownTurn(100, 10, cost)
	t.OutputDialect = workeroutput.DialectClaudeStreamJSON
	t.UsageNormalization.CostBasis = basis
	return t
}

// Unknown cost coverage persisted by migration stays unknown during accounting.
func TestUnknownClaudeCostIsUnknownAndStepCostIsKept(t *testing.T) {
	s := rollupStore(t)
	meta := Meta{ID: "ses_20261019-120000-aaaaaaaa", RoomID: "r1"}
	old, step := claudeTurn("", 159.85), claudeTurn(workeroutput.CostBasisTurn, 0.25)
	old.N, step.N = 1, 2
	old.UsageNormalization.Cost = "unknown"
	for _, turn := range []Turn{old, step} {
		if err := s.RecordTurnUsage(meta, turn); err != nil {
			t.Fatal(err)
		}
	}
	got := readAll(t, s).Totals.Cost
	if got.USD == nil || *got.USD != 0.25 || got.CompleteTurns != 1 || got.UnknownTurns != 1 {
		t.Fatalf("cost = %+v", got)
	}
}

func cachedTurn(in, cached int64, input, cachedState string) Turn {
	t := knownTurn(in, 1, 0)
	t.Usage.CachedTokens = cached
	t.UsageNormalization.Input, t.UsageNormalization.CachedInput = input, cachedState
	return t
}

// A cached count above input breaks the subset rule (older Antigravity turns
// counted cache reads beside input), so that turn's cache reads as unknown.
func TestCachedAboveInputIsUnknown(t *testing.T) {
	s := rollupStore(t)
	meta := Meta{ID: "ses_20261019-120000-aaaaaaaa", RoomID: "r1"}
	bad, ok := cachedTurn(100, 900, "complete", "complete"), cachedTurn(100, 80, "complete", "complete")
	bad.N, ok.N = 1, 2
	for _, turn := range []Turn{bad, ok} {
		if err := s.RecordTurnUsage(meta, turn); err != nil {
			t.Fatal(err)
		}
	}
	got := readAll(t, s).Totals
	if *got.Cached.Tokens != 80 || got.Cached.CompleteTurns != 1 || got.Cached.UnknownTurns != 1 {
		t.Fatalf("cached = %+v", got.Cached)
	}
}

// The cache rate needs input and cache summed over the same turns.
func TestCachePairedSumsCoverOnlyTurnsThatReportBoth(t *testing.T) {
	s := rollupStore(t)
	meta := Meta{ID: "ses_20261019-120000-aaaaaaaa", RoomID: "r1"}
	both, noCache, partial := cachedTurn(100, 90, "complete", "complete"), cachedTurn(900, 0, "complete", "unknown"), cachedTurn(50, 5, "complete", "partial")
	both.N, noCache.N, partial.N = 1, 2, 3
	for _, turn := range []Turn{both, noCache, partial} {
		if err := s.RecordTurnUsage(meta, turn); err != nil {
			t.Fatal(err)
		}
	}
	got := readAll(t, s).Totals.CachePaired
	if got.Turns != 1 || got.Input != 100 || got.Cached != 90 {
		t.Fatalf("paired = %+v", got)
	}
}

// Rows written by an older rollup layout are rebuilt from the turn files.
func TestOutdatedRollupVersionIsRebuilt(t *testing.T) {
	s := rollupStore(t)
	seedRollupSession(t, s, "ses_20261019-120000-aaaaaaaa", "r1", "", claudeTurn("", 3.0))
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	db, _ := s.usageDB()
	if _, err := db.Exec(`UPDATE meta SET value='1' WHERE key='usage.rollup.version'`); err != nil {
		t.Fatal(err)
	}
	// A restarted engine opens a fresh handle and checks the version again.
	if err := database.Close(filepath.Dir(s.root())); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s).Totals.Turns; got != 0 {
		t.Fatalf("outdated rows kept: %d turns", got)
	}
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s).Totals; got.Turns != 1 || got.Cost.USD == nil || *got.Cost.USD != 3.0 {
		t.Fatalf("rebuilt totals = %+v", got)
	}
}

// The refold gives old Claude turns their own spend from the saved cumulative
// totals. This is the real sequence from session ses_20261018-220036-b3c02a1e.
func TestRefoldTurnsOldClaudeCumulativeCostIntoSteps(t *testing.T) {
	s := rollupStore(t)
	var turns []Turn
	for _, total := range []float64{3.365, 3.547, 3.584, 4.051, 1.0, 1.25} { // the 1.0 is a counter reset
		turn := claudeTurn("", total)
		turn.HarnessSession = "native-1"
		turns = append(turns, turn)
	}
	turns[2].UsageNormalization.Cost = "unknown" // a turn with no cost report breaks the chain
	seedRollupSession(t, s, "ses_20261019-120000-aaaaaaaa", "r1", "", turns...)
	if err := Migrate(filepath.Dir(s.root())); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, s).Totals.Cost
	// turn 1: 3.365 (starts at zero), turn 2: 0.182, turn 3: unknown, turn 4: unknown (no complete
	// previous turn), turn 5: unknown (reset), turn 6: 0.25
	want := 3.365 + (3.547 - 3.365) + (1.25 - 1.0)
	if got.USD == nil || *got.USD < want-1e-9 || *got.USD > want+1e-9 || got.CompleteTurns != 3 || got.UnknownTurns != 3 {
		t.Fatalf("cost = %+v want %v", got, want)
	}
}

// A transfer starts a new native session while the Dyna turn numbers go on, so
// totals from different native sessions must never be subtracted.
func TestRefoldNeverSubtractsAcrossNativeSessions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		second float64
	}{{"increasing", 5.0}, {"decreasing", 1.0}} {
		t.Run(tc.name, func(t *testing.T) {
			s := rollupStore(t)
			a, b := claudeTurn("", 3.0), claudeTurn("", tc.second)
			a.HarnessSession, b.HarnessSession = "native-old", "native-new"
			seedRollupSession(t, s, "ses_20261019-120000-aaaaaaaa", "r1", "", a, b)
			if err := Migrate(filepath.Dir(s.root())); err != nil {
				t.Fatal(err)
			}
			if err := s.ReconcileUsage(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := readAll(t, s).Totals.Cost
			if got.USD == nil || *got.USD != 3.0 || got.CompleteTurns != 1 || got.UnknownTurns != 1 {
				t.Fatalf("cost = %+v", got)
			}
		})
	}
	// No recorded native session on the later turn is also an uncertain boundary.
	s := rollupStore(t)
	a, b := claudeTurn("", 3.0), claudeTurn("", 5.0)
	a.HarnessSession = "native-old"
	seedRollupSession(t, s, "ses_20261019-120000-bbbbbbbb", "r1", "", a, b)
	if err := Migrate(filepath.Dir(s.root())); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, s).Totals.Cost; got.CompleteTurns != 1 || got.UnknownTurns != 1 {
		t.Fatalf("cost = %+v", got)
	}
}

// Partial input is a lower bound, so a larger valid cache count is not an error.
func TestPartialInputKeepsTheCacheCountAndStaysOutOfPairs(t *testing.T) {
	s := rollupStore(t)
	meta := Meta{ID: "ses_20261019-120000-aaaaaaaa", RoomID: "r1"}
	turn := cachedTurn(100, 180, "partial", "complete")
	turn.N = 1
	if err := s.RecordTurnUsage(meta, turn); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, s).Totals
	if got.Cached.Tokens == nil || *got.Cached.Tokens != 180 || got.Cached.CompleteTurns != 1 || got.CachePaired.Turns != 0 {
		t.Fatalf("cached = %+v paired = %+v", got.Cached, got.CachePaired)
	}
}
