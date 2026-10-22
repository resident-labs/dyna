package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

type quotaObservationFixture struct {
	service *Service
	store   *accountstore.Store
	db      *sql.DB
	first   attempts.Fact
	second  attempts.Fact
	now     time.Time
}

func newQuotaObservationFixture(t *testing.T) quotaObservationFixture {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	registry := &accountstore.Store{Root: filepath.Join(dir, "accounts")}
	var first, second accountstore.Metadata
	if err := registry.Update(func(st *accountstore.State) error {
		var err error
		first, err = st.Save("muse", "", "identity-a", "A", accountstore.Credential{Schema: "fixture", Payload: json.RawMessage(`{"token":"secret-a"}`)})
		if err != nil {
			return err
		}
		second, err = st.Save("muse", "", "identity-b", "B", accountstore.Credential{Schema: "fixture", Payload: json.RawMessage(`{"token":"secret-b"}`)})
		st.Find(first.ID).Observation = &accountstore.Observation{Plan: &accountstore.Plan{ID: "pro"}, Windows: []accountstore.Window{{ID: "weekly"}, {ID: "subscription"}}, Failures: 3, UsageError: "existing", Unavailable: true}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetActive("muse", second.ID); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Only the canonical parent keys matter to the immutable ledger here.
	if _, err := db.Exec(`CREATE TABLE session_turns (session TEXT, n INTEGER, revision INTEGER, turn BLOB, PRIMARY KEY(session,n));
CREATE TABLE runs (id TEXT PRIMARY KEY);
INSERT INTO session_turns VALUES ('ses_quota',1,1,'{"status":"ok"}');`); err != nil {
		t.Fatal(err)
	}
	if err := attempts.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if err := state.MigrateRequests(dir); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC)
	fact := func(n string, meta accountstore.Metadata) attempts.Fact {
		return attempts.Fact{ExecutionRef: callspec.ExecutionRef{Session: "ses_quota", Turn: 1, AdmissionID: "ses_quota/1/native/" + n}, AccountID: meta.ID, Identity: meta.Identity, Profile: "muse", Harness: "muse", WorkDir: "/repo", AdmittedAt: now.Add(-time.Minute)}
	}
	f := quotaObservationFixture{service: New(Options{Store: registry}), store: registry, db: db, first: fact("1", first), second: fact("2", second), now: now}
	if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		for _, fact := range []attempts.Fact{f.first, f.second} {
			if _, err := attempts.SaveTx(t.Context(), conn, fact); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f quotaObservationFixture) input() callspec.MuseQuotaObservation {
	return callspec.MuseQuotaObservation{ExecutionRef: f.first.ExecutionRef, Identity: f.first.Identity, ObservedAt: f.now, ResetsAt: f.now.Add(time.Hour)}
}

type quotaCredentialFile struct {
	Content  string
	Mode     os.FileMode
	Modified time.Time
}

func quotaCredentialFiles(t *testing.T, root string) map[string]quotaCredentialFile {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "credentials", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]quotaCredentialFile{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		out[file] = quotaCredentialFile{Content: string(raw), Mode: info.Mode(), Modified: info.ModTime()}
	}
	return out
}

func TestMuseQuotaUsesHistoricalAccountAndPreservesCredentials(t *testing.T) {
	f := newQuotaObservationFixture(t)
	before := quotaCredentialFiles(t, f.store.Root)
	request := func(fingerprint string) context.Context {
		return state.ForRequestCall(t.Context(), "quota-observation", "ObserveMuseQuota", []byte(fingerprint))
	}
	if err := f.service.ObserveMuseQuota(request("original"), f.input()); err != nil {
		t.Fatal(err)
	}
	st, err := f.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	o := st.Find(f.first.AccountID).Observation
	if o == nil || o.Plan == nil || o.Plan.ID != "pro" || len(o.Windows) != 2 || o.Windows[0].ID != "weekly" || o.Windows[1].ID != "subscription" || o.Windows[1].UsedPercent == nil || *o.Windows[1].UsedPercent != 100 || !o.ObservedAt.Equal(f.now) || o.RateLimitedUntil == nil || !o.RateLimitedUntil.Equal(f.input().ResetsAt) || o.Unavailable || o.Failures != 3 || o.UsageError != "existing" {
		t.Fatalf("special Muse merge changed: %+v", o)
	}
	if st.Find(f.second.AccountID).Observation != nil {
		t.Fatal("delayed observation followed the active account")
	}
	if err := f.service.ObserveMuseQuota(request("original"), f.input()); !errors.Is(err, state.ErrRepeated) {
		t.Fatalf("replay = %v", err)
	}
	changed := f.input()
	changed.ResetsAt = changed.ResetsAt.Add(time.Hour)
	if err := f.service.ObserveMuseQuota(request("changed"), changed); !errors.Is(err, state.ErrRequestConflict) {
		t.Fatalf("changed arguments = %v", err)
	}
	if !reflect.DeepEqual(before, quotaCredentialFiles(t, f.store.Root)) {
		t.Fatal("observation, replay or conflict changed credential files")
	}
}

func TestMuseQuotaRefusesChangedFactsAndInvalidObservations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*callspec.MuseQuotaObservation)
	}{
		{"identity", func(in *callspec.MuseQuotaObservation) { in.Identity = "identity-b" }},
		{"missing admission", func(in *callspec.MuseQuotaObservation) { in.AdmissionID = "" }},
		{"unknown admission", func(in *callspec.MuseQuotaObservation) { in.AdmissionID += "/missing" }},
		{"other turn", func(in *callspec.MuseQuotaObservation) { in.Turn++ }},
		{"missing time", func(in *callspec.MuseQuotaObservation) { in.ObservedAt = time.Time{} }},
		{"invalid reset", func(in *callspec.MuseQuotaObservation) { in.ResetsAt = in.ObservedAt }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuotaObservationFixture(t)
			before := quotaCredentialFiles(t, f.store.Root)
			in := f.input()
			tc.edit(&in)
			if err := f.service.ObserveMuseQuota(t.Context(), in); err == nil {
				t.Fatal("invalid observation succeeded")
			}
			if !reflect.DeepEqual(before, quotaCredentialFiles(t, f.store.Root)) {
				t.Fatal("refusal changed credential files")
			}
		})
	}
}

func TestMuseQuotaRefusesChangedOrRemovedSavedAccount(t *testing.T) {
	for _, change := range []string{"identity", "harness", "removed"} {
		t.Run(change, func(t *testing.T) {
			f := newQuotaObservationFixture(t)
			if err := f.store.Update(func(st *accountstore.State) error {
				switch change {
				case "identity":
					st.Find(f.first.AccountID).Identity = "replacement"
				case "harness":
					st.Find(f.first.AccountID).Harness = "codex"
				case "removed":
					st.Remove(f.first.AccountID)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			before := quotaCredentialFiles(t, f.store.Root)
			if err := f.service.ObserveMuseQuota(t.Context(), f.input()); err == nil {
				t.Fatal("changed saved account received an observation")
			}
			if !reflect.DeepEqual(before, quotaCredentialFiles(t, f.store.Root)) {
				t.Fatal("refusal changed credential files")
			}
			st, err := f.store.Read()
			if err != nil || st.Find(f.second.AccountID).Observation != nil {
				t.Fatal("observation redirected to account B", err)
			}
		})
	}
}

func TestMuseQuotaPreservesNewerObservation(t *testing.T) {
	f := newQuotaObservationFixture(t)
	newer := f.now.Add(10 * time.Minute)
	if err := f.store.Update(func(st *accountstore.State) error {
		st.Find(f.first.AccountID).Observation.ObservedAt = newer
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.ObserveMuseQuota(t.Context(), f.input()); err != nil {
		t.Fatal(err)
	}
	after, err := f.store.Read()
	if err != nil || !reflect.DeepEqual(before.Find(f.first.AccountID).Observation, after.Find(f.first.AccountID).Observation) {
		t.Fatal("older observation replaced a newer reading", err)
	}
	in := f.input()
	in.ObservedAt = newer
	if err := f.service.ObserveMuseQuota(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	after, err = f.store.Read()
	if err != nil || after.Find(f.first.AccountID).Observation.RateLimitedUntil == nil {
		t.Fatal("equal-time observation behavior changed", err)
	}
}
