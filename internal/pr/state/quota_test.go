package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyQuotaMigrationKeepsOnlyProvenPause(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "known"}[known], func(t *testing.T) {
			root := newRoot(t)
			until := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			scope := RateLimitScope{Host: "GITHUB.COM", Account: "Alice", Resource: "CORE"}
			remaining := 0
			old := RateLimit{Remaining: &remaining, Reset: until, PauseUntil: until, Reason: "GitHub rate limit", SecondaryFailures: 3}
			if known {
				old.PauseScope = scope
			}
			raw, err := json.Marshal(struct {
				Version   int       `json:"version"`
				RateLimit RateLimit `json:"rateLimit"`
			}{1, old})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			st, err := Read(root)
			if err != nil {
				t.Fatal(err)
			}
			if known {
				rate := st.RateLimits[scope.Key()]
				if !rate.PauseUntil.Equal(until) || rate.Remaining != nil || !rate.Reset.IsZero() || rate.SecondaryFailures != 0 {
					t.Fatalf("mixed accounting migrated: %+v", rate)
				}
				if !st.LegacyPause.IsZero() {
					t.Fatal("known scope became global")
				}
			} else if len(st.RateLimits) != 0 || !st.LegacyPause.IsZero() {
				t.Fatalf("unknown scope: %+v", st)
			}
			if err := Write(root, st); err != nil {
				t.Fatal(err)
			}
			forgetSnapshot()
			st, err = Read(root)
			if err != nil {
				t.Fatal(err)
			}
			if known && !st.RateLimit.PauseUntil.Equal(until) {
				t.Fatal("restart lost original pause")
			}
			st.ExpireLegacyPause(until.Add(-time.Second))
			if !known && !st.LegacyPause.IsZero() {
				t.Fatal("unknown pause ended early")
			}
			st.ExpireLegacyPause(until)
			if !st.LegacyPause.IsZero() {
				t.Fatal("unknown pause did not expire")
			}
		})
	}
}

func TestQuotaDisplayIsDerivedAfterDiskRestart(t *testing.T) {
	root := newRoot(t)
	n := 123
	scope := RateLimitScope{Host: "github.com", Account: "alice", Resource: "core"}
	st := State{Version: 1, RateLimits: map[string]RateLimit{scope.Key(): {Remaining: &n, Reset: time.Now().Add(time.Hour)}}, Records: map[string]*Record{"a": {Key: "a", Host: "GITHUB.COM", QuotaAccount: "ALICE", State: "open"}, "b": {Key: "b", Host: "git.example", QuotaAccount: "alice", State: "open"}}}
	if err := Write(root, st); err != nil {
		t.Fatal(err)
	}
	forgetSnapshot()
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Records["a"].RateLimit.Remaining == nil || *got.Records["a"].RateLimit.Remaining != n || got.Records["b"].RateLimit.Remaining != nil {
		t.Fatalf("scope display: %+v", got.Records)
	}
	rows, err := mustOpen(t, root).Query("SELECT body FROM pr_rows")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var disk struct{ Record map[string]json.RawMessage }
		if err := json.Unmarshal(raw, &disk); err != nil {
			t.Fatal(err)
		}
		if _, found := disk.Record["rateLimit"]; found {
			t.Fatal("derived display persisted in record")
		}
	}
}
