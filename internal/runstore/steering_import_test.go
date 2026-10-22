package runstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func TestDeactivatedSteeringCannotBeResurrectedByFile(t *testing.T) {
	for _, archived := range []bool{false, true} {
		name := "live"
		if archived {
			name = "custom-archive"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv(ArchiveDirEnv, t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			dataDir := store.DataDir()
			t.Cleanup(func() { database.Close(dataDir) })
			id := "wf_steering-import"
			dir, err := agentSteeringDir(id, 1)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			started := time.Unix(100, 0)
			meta := Meta{ID: id, Status: "running", Pid: os.Getpid(), StartedAt: started}
			legacy := steeringState{RunID: id, AgentID: 1, Pid: meta.Pid, StartedAt: started.UnixNano(), Active: true}
			write := func(path string, value any) []byte {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			write(filepath.Join(RunsDir(), id, "meta.json"), meta)
			path := filepath.Join(dir, "steering-state.json")
			active := write(path, legacy)
			queue := filepath.Join(dir, "steering.jsonl")
			before := []byte("{\"message\":\"retained\"}\n")
			if err := os.WriteFile(queue, before, 0600); err != nil {
				t.Fatal(err)
			}
			legacyDir := filepath.Join(ArchiveDir(), id)
			if archived {
				if err := os.Rename(filepath.Join(RunsDir(), id), legacyDir); err != nil {
					t.Fatal(err)
				}
			}
			if err := Migrate(dataDir); err != nil {
				t.Fatal(err)
			}
			db, err := runsDB()
			if err != nil {
				t.Fatal(err)
			}
			var importedActive bool
			if err := db.QueryRow("SELECT active FROM run_agent_steering WHERE run=? AND agent=1", id).Scan(&importedActive); err != nil || !importedActive {
				t.Fatalf("startup import = %v, %v", importedActive, err)
			}
			if archived {
				if err := os.Rename(legacyDir, filepath.Join(RunsDir(), id)); err != nil {
					t.Fatal(err)
				}
			}
			if s, err := readSteeringState(dir, id, 1); err != nil || !s.Active {
				t.Fatalf("legacy activation = %#v, %v", s, err)
			}
			if err := DeactivateAgentSteering(id, 1); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(active, []byte("\n  \n")...), 0600); err != nil {
				t.Fatal(err)
			}
			if err := SubmitAgentSteering(id, 1, "must reject"); err == nil {
				t.Fatal("stale file resurrected deactivated worker")
			}
			var storedActive bool
			if err := db.QueryRow("SELECT active FROM run_agent_steering WHERE run=? AND agent=1", id).Scan(&storedActive); err != nil || storedActive {
				t.Fatalf("stored active = %v, %v", storedActive, err)
			}
			after, err := os.ReadFile(queue)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("queue changed: %q, %v", after, err)
			}
		})
	}
}
