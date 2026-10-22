package engineclient

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func TestSharedSpoolKeepsSessionAndWorkflowQueueIDs(t *testing.T) {
	dir := storetest.DataDir(t)
	if err := state.MigrateRequests(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	for _, owner := range []string{"ses_shared", "wf_shared"} {
		t.Run(owner, func(t *testing.T) {
			queued := []Queued{
				{ID: owner + "/123/456/1", Method: "AdmitNativeAttempt", Args: []any{map[string]any{"profile": "native", "number": 7}}},
				{ID: owner + "/123/456/2", Method: "ObserveMuseQuota", Args: []any{"observation"}},
			}
			before := time.Now()
			if err := AppendRunnerSpool(t.Context(), state.For(db), dir, owner, queued); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(sessionlife.RunnerSpoolDir(dir), owner+".jsonl")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != len(queued) {
				t.Fatalf("got %d lines, want %d", len(lines), len(queued))
			}
			for i, raw := range lines {
				var line SpoolLine
				if err := json.Unmarshal([]byte(raw), &line); err != nil {
					t.Fatal(err)
				}
				if line.ID != queued[i].ID || line.Method != queued[i].Method || line.At.Before(before) || line.At.After(time.Now()) {
					t.Fatalf("queue identity/order changed: %+v", line)
				}
				var expected []json.RawMessage
				for _, value := range queued[i].Args {
					b, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					expected = append(expected, b)
				}
				if !reflect.DeepEqual(line.Args, expected) {
					t.Fatalf("arguments changed: %s, want %s", line.Args, expected)
				}
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("spool mode = %v, %v", info, err)
			}
		})
	}
}
