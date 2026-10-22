package service

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

type claudeUsageFixture struct {
	service *Service
	store   *accountstore.Store
	db      *sql.DB
	fact    attempts.Fact
	now     time.Time
}

func newClaudeUsageFixture(t *testing.T) claudeUsageFixture {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	registry := &accountstore.Store{Root: filepath.Join(dir, "accounts")}
	var meta accountstore.Metadata
	if err := registry.Update(func(st *accountstore.State) error {
		var err error
		meta, err = st.Save("claude-code", "", "identity-a", "A", accountstore.Credential{Schema: "fixture", Payload: json.RawMessage(`{"token":"secret-a"}`)})
		if err != nil {
			return err
		}
		used := 80.0
		st.Find(meta.ID).Observation = &accountstore.Observation{
			Plan:        &accountstore.Plan{ID: "max", Label: "max"},
			Windows:     []accountstore.Window{{ID: "five_hour", Label: "Session (5-hour)", UsedPercent: &used, Unit: "percent", ResetsAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)}},
			UsageError:  "existing",
			Failures:    3,
			Unavailable: true,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE session_turns (session TEXT, n INTEGER, revision INTEGER, turn BLOB, PRIMARY KEY(session,n));
CREATE TABLE runs (id TEXT PRIMARY KEY);
INSERT INTO session_turns VALUES ('ses_quota',1,1,'{"status":"ok"}');`); err != nil {
		t.Fatal(err)
	}
	if err = attempts.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if err = state.MigrateRequests(dir); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC)
	f := claudeUsageFixture{service: New(Options{Store: registry}), store: registry, db: db, now: now}
	f.fact = attempts.Fact{
		ExecutionRef: callspec.ExecutionRef{Session: "ses_quota", Turn: 1, AdmissionID: "ses_quota/1/native/1"},
		AccountID:    meta.ID, Identity: meta.Identity, Profile: "claude", Harness: "claude-code", WorkDir: "/repo", AdmittedAt: now.Add(-time.Minute),
	}
	f.saveFact(t, f.fact)
	return f
}

func (f claudeUsageFixture) saveFact(t *testing.T, fact attempts.Fact) {
	t.Helper()
	if err := state.For(f.db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := attempts.SaveTx(t.Context(), conn, fact)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (f claudeUsageFixture) saved() *accountstore.Observation {
	st, err := f.store.Read()
	if err != nil {
		panic(err)
	}
	return st.Find(f.fact.AccountID).Observation
}

func (f claudeUsageFixture) input() callspec.ClaudeUsageObservation {
	return callspec.ClaudeUsageObservation{
		ExecutionRef: f.fact.ExecutionRef, Identity: f.fact.Identity, ObservedAt: f.now,
		Windows: []callspec.ClaudeUsageWindow{
			{ID: "five_hour", UsedPercent: 18, ResetsAt: f.now.Add(4 * time.Hour)},
			{ID: "seven_day", UsedPercent: 15, ResetsAt: f.now.Add(6 * 24 * time.Hour)},
		},
	}
}

// A rate_limit_event updates the account's reading: the five-hour window is
// replaced, the weekly window appears, and the plan stays. The merged reading
// feeds the account score.
func TestObserveClaudeUsageUpdatesTheReading(t *testing.T) {
	f := newClaudeUsageFixture(t)
	if err := f.service.ObserveClaudeUsage(t.Context(), f.input()); err != nil {
		t.Fatal(err)
	}
	o := f.saved()
	if o == nil || o.UsageError != "" || o.Unavailable {
		t.Fatalf("observation = %#v, want the error and unavailable marks cleared", o)
	}
	if !o.ObservedAt.Equal(f.now) || !o.LastAttemptAt.Equal(f.now) {
		t.Fatalf("observation times = %v/%v, want %v", o.ObservedAt, o.LastAttemptAt, f.now)
	}
	if o.Plan == nil || o.Plan.ID != "max" {
		t.Fatalf("plan = %#v, want the saved plan preserved", o.Plan)
	}
	byID := map[string]accountstore.Window{}
	for _, w := range o.Windows {
		byID[w.ID] = w
	}
	if w := byID["five_hour"]; w.UsedPercent == nil || *w.UsedPercent != 18 {
		t.Fatalf("five_hour = %#v, want 18%% from the event", w)
	}
	if w := byID["seven_day"]; w.UsedPercent == nil || *w.UsedPercent != 15 {
		t.Fatalf("seven_day = %#v, want 15%% from the event", w)
	}
	if got, want := accountstore.Score(o, f.now), (1-0.15)*5/(6*6); got != want {
		t.Fatalf("score = %v, want %v from the live reading", got, want)
	}
}

func TestObserveClaudeUsageKeepsANewerProbeAndRejectsBadInput(t *testing.T) {
	f := newClaudeUsageFixture(t)
	if err := f.service.ObserveClaudeUsage(t.Context(), f.input()); err != nil {
		t.Fatal(err)
	}
	older := f.input()
	older.ObservedAt = f.now.Add(-time.Hour)
	older.Windows = []callspec.ClaudeUsageWindow{{ID: "seven_day", UsedPercent: 90, ResetsAt: f.now.Add(24 * time.Hour)}}
	if err := f.service.ObserveClaudeUsage(t.Context(), older); err != nil {
		t.Fatal(err)
	}
	if o := f.saved(); o == nil {
		t.Fatal("the reading was lost")
	} else if w := accountstore.WeeklyWindow(o); w == nil || w.UsedPercent == nil || *w.UsedPercent != 15 {
		t.Fatalf("older reading overwrote the newer probe: %#v", w)
	}
	withWindows := func(windows ...callspec.ClaudeUsageWindow) callspec.ClaudeUsageObservation {
		in := f.input()
		in.Windows = windows
		return in
	}
	for name, in := range map[string]callspec.ClaudeUsageObservation{
		"another identity": func() callspec.ClaudeUsageObservation { in := f.input(); in.Identity = "other"; return in }(),
		"no windows":       {},
		"invalid percent":  withWindows(callspec.ClaudeUsageWindow{ID: "seven_day", UsedPercent: 150, ResetsAt: f.now.Add(time.Hour)}),
		"missing reset":    withWindows(callspec.ClaudeUsageWindow{ID: "seven_day", UsedPercent: 15}),
	} {
		if err := f.service.ObserveClaudeUsage(t.Context(), in); err == nil {
			t.Fatalf("%s: the observation was accepted", name)
		}
	}
}

func TestObserveClaudeUsageRequiresTheAdmittedClaudeAccount(t *testing.T) {
	f := newClaudeUsageFixture(t)
	in := f.input()
	// An admission of another harness must not carry a Claude reading.
	fact := f.fact
	fact.Harness = "codex"
	fact.AdmissionID = "ses_quota/1/native/2"
	f.saveFact(t, fact)
	in.ExecutionRef = fact.ExecutionRef
	if err := f.service.ObserveClaudeUsage(t.Context(), in); err == nil {
		t.Fatal("an observation that differs from the admitted account was accepted")
	}
	if o := f.saved(); o == nil || o.UsageError != "existing" || o.Unavailable != true {
		t.Fatalf("a rejected observation changed the saved reading: %#v", o)
	}
}
