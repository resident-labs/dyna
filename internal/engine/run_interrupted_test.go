package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func TestSettleInterruptedByUpdate(t *testing.T) {
	since := time.UnixMilli(1000)
	failed := runstore.Event{T: "agent_end", ID: 1, TS: 2000, Status: "error", Error: "no such table"}
	handled := runstore.Event{T: "agent_end", ID: 4, TS: 500, Status: "error", Error: "optional brief failed"}
	ordinary := runstore.Event{T: "agent_end", ID: 5, TS: 2000, Status: "error", Error: "optional brief failed"}
	ok := runstore.Event{T: "agent_end", ID: 2, TS: 2000, Status: "ok"}
	cached := runstore.Event{T: "agent_end", ID: 3, TS: 2000, Status: "error", Cached: true}
	const msg = "interrupted by update from v1 to v2; resume it with dyna run <script.js> --resume wf_settle"
	for _, tc := range []struct {
		name       string
		build      string
		status     string
		events     []runstore.Event
		wantStatus string
		wantError  string
	}{
		{"other build, failed agent", "v1", "ok", []runstore.Event{ok, failed}, runstore.StatusInterrupted, msg},
		{"other build, run already errored", "v1", "error", []runstore.Event{failed}, runstore.StatusInterrupted, msg},
		{"no recorded build counts as another", "", "ok", []runstore.Event{failed}, runstore.StatusInterrupted, strings.Replace(msg, "v1", unknownBuild, 1)},
		{"other build, all agents ok", "v1", "ok", []runstore.Event{ok}, "ok", ""},
		{"other build, only a cached failure", "v1", "ok", []runstore.Event{cached}, "ok", ""},
		{"other build, canceled stays canceled", "v1", "canceled", []runstore.Event{failed}, "canceled", ""},
		{"same build, failed agent", "v2", "ok", []runstore.Event{failed}, "ok", ""},
		{"other build, failure handled before the update", "v1", "ok", []runstore.Event{handled, ok}, "ok", ""},
		{"other build, ordinary failure handled after the update", "v1", "ok", []runstore.Event{ordinary, ok}, "ok", ""},
		{"other build, events unreadable", "v1", "ok", nil, "ok", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := storetest.DataDir(t)
			if err := MigrateStores(dir); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			const run = "wf_settle"
			meta := runstore.Meta{ID: run, Status: "running", Build: tc.build, StartedAt: time.Now()}
			body, _ := json.Marshal(meta)
			if _, err := db.Exec("INSERT INTO runs(id,session,started,body) VALUES(?,?,?,?)", run, "", meta.StartedAt.UnixNano(), string(body)); err != nil {
				t.Fatal(err)
			}
			var lines []string
			for _, e := range tc.events {
				line, _ := json.Marshal(e)
				lines = append(lines, string(line))
			}
			// A nil event list leaves no events file, which cannot be read.
			if tc.events != nil {
				if err := os.MkdirAll(runstore.RunDir(run), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(runstore.RunDir(run), "events.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := settleInterrupted(t.Context(), state.For(db), callspec.RunFinish{Run: run, Status: tc.status}, "v2", since)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus || got.Error != tc.wantError {
				t.Fatalf("got %q %q, want %q %q", got.Status, got.Error, tc.wantStatus, tc.wantError)
			}
		})
	}
}
