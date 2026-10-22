package subagent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
)

// The fixture copies a retained queue with claimed, cancelled and dismissed
// entries. Only message IDs changed; private source evidence stays in scratch.
func TestMigrateImportsRetainedQueueOnce(t *testing.T) {
	data := t.TempDir()
	t.Cleanup(func() { database.Close(data) })
	id := NewID()
	dir := filepath.Join(data, "sessions", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/retained-queue.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, queueFile)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateQueues(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	// Query the row before readQueue can trigger the old lazy importer.
	stored, version, err := queryQueueRow(db, id)
	if err != nil {
		t.Fatalf("startup did not import retained queue: %v", err)
	}
	var want, got queueClaims
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stored, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("import changed queue claims: %+v", got)
	}
	preserved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, preserved) {
		t.Fatalf("migration changed retained source: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(data); err != nil {
		t.Fatal(err)
	}
	if err := migrateQueues(data); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	after, afterVersion, err := queryQueueRow(db, id)
	if err != nil || !bytes.Equal(stored, after) || version != afterVersion {
		t.Fatalf("replay changed retained queue: version %q to %q, %v", version, afterVersion, err)
	}
}

func TestQueueMigrationPreservesCanonicalRow(t *testing.T) {
	for _, source := range []string{"owned", "unstamped", "damaged", "emptied"} {
		t.Run(source, func(t *testing.T) {
			data := t.TempDir()
			t.Cleanup(func() { database.Close(data) })
			id := NewID()
			dir := filepath.Join(data, "sessions", id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile("testdata/retained-queue.json")
			if err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			seedQueueSchemaV1(t, db)
			path := filepath.Join(dir, queueFile)
			if source == "owned" {
				if err := state.MigrateMirrors(data); err != nil {
					t.Fatal(err)
				}
				if err := state.For(db).Mirror(t.Context(), sessionlife.QueueMirrorKey(id), path, func(w io.Writer) error {
					_, err := w.Write(raw)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				stale := raw
				if source == "damaged" {
					stale = []byte("{")
				}
				if err := os.WriteFile(path, stale, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var newer queueClaims
			if err := json.Unmarshal(raw, &newer); err != nil {
				t.Fatal(err)
			}
			newer.Claimed["msg_newer_canonical_claim"] = 999
			current, err := json.Marshal(newer)
			if err != nil {
				t.Fatal(err)
			}
			if source == "emptied" {
				current = []byte("{}")
			}
			if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
				_, err := saveQueueRow(t.Context(), conn, id, current)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, version, err := queryQueueRow(db, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := migrateQueues(data); err != nil {
				t.Fatal(err)
			}
			stored, afterVersion, err := queryQueueRow(db, id)
			if err != nil || !bytes.Equal(stored, current) || version != afterVersion {
				t.Fatalf("retained file replaced current canonical queue: %q to %q, %v", version, afterVersion, err)
			}

		})
	}
}

func TestQueueMigrationRollsBackAndRetriesMalformedSource(t *testing.T) {
	data := t.TempDir()
	t.Cleanup(func() { database.Close(data) })
	raw, err := os.ReadFile("testdata/retained-queue.json")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"ses_20261021-000000-00000000", "ses_20261021-000000-ffffffff"}
	for i, id := range ids {
		dir := filepath.Join(data, "sessions", id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		source := raw
		if i == 1 {
			source = []byte("{")
		}
		if err := os.WriteFile(filepath.Join(dir, queueFile), source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	seedQueueSchemaV1(t, db)
	badPath := filepath.Join(data, "sessions", ids[1], queueFile)
	if err := migrateQueues(data); err == nil || !strings.Contains(err.Error(), badPath) {
		t.Fatalf("malformed import error = %v; want source path", err)
	}
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_queues").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed migration retained %d queue rows: %v", rows, err)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='subagent.queues'").Scan(&version); err != nil || version != "1" {
		t.Fatalf("failed import committed version %q: %v", version, err)
	}
	if err := os.WriteFile(badPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := migrateQueues(data); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM session_queues").Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("repaired migration imported %d queue rows: %v", rows, err)
	}
}

func seedQueueSchemaV1(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := database.Migrate(db, "subagent.queues", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), queueSchema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostMigrationRunnerClaimKeepsCanonicalQueueAndReceipt(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		name := "newer"
		if cleared {
			name = "cleared"
		}
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			priorID := ""
			if !cleared {
				priorID = postClaimInput(t, e, info, "cancelled earlier claim").ID
			}
			msg := postClaimInput(t, e, info, "claim from canonical state")
			dir, err := e.m.Store.Dir(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			data := filepath.Dir(e.m.Store.root())
			db, err := queuesDB(e.m.Store.root())
			if err != nil {
				t.Fatal(err)
			}
			current := []byte("{}")
			if !cleared {
				current, err = json.Marshal(queueClaims{Claimed: map[string]int{priorID: 1}, Cancelled: map[string]bool{priorID: true}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
				_, err := saveQueueRow(t.Context(), conn, info.ID, current)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Keep the real retained queue shape and add a synthetic stale
			// dismissal for this current room message.
			raw, err := os.ReadFile("testdata/retained-queue.json")
			if err != nil {
				t.Fatal(err)
			}
			var stale queueClaims
			if err := json.Unmarshal(raw, &stale); err != nil {
				t.Fatal(err)
			}
			stale.Dismissed[msg.ID] = true
			raw, err = json.Marshal(stale)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, queueFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("DELETE FROM state_mirrors WHERE key=?", sessionlife.QueueMirrorKey(info.ID)); err != nil {
				t.Fatal(err)
			}
			if err := migrateQueues(data); err != nil {
				t.Fatal(err)
			}
			if err := state.MigrateRequests(data); err != nil {
				t.Fatal(err)
			}
			actor := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: info.ID})
			rid := info.ID + "/post-migration-claim"
			ctx := state.ForRequestCall(callspec.WithRequest(actor, rid), "runner/"+rid, "RunnerClaimInput", []byte("fixture-call"))
			reply, err := e.m.RunnerClaimInput(ctx, InputClaim{Turn: 1, Startup: true})
			if err != nil || len(reply.Posts) != 1 || reply.Posts[0].Steer.ID != msg.ID {
				t.Fatalf("post-migration claim used stale state: %+v, %v", reply, err)
			}
			stored, revision, err := queryQueueRow(db, info.ID)
			if err != nil {
				t.Fatal(err)
			}
			var got queueClaims
			if err := json.Unmarshal(stored, &got); err != nil {
				t.Fatal(err)
			}
			wantClaims, wantCancelled := 1, 0
			if !cleared {
				wantClaims, wantCancelled = 2, 1
			}
			if got.Claimed[msg.ID] != 1 || len(got.Claimed) != wantClaims || len(got.Dismissed) != 0 || len(got.Cancelled) != wantCancelled || !cleared && (got.Claimed[priorID] != 1 || !got.Cancelled[priorID]) {
				t.Fatalf("claim resurrected stale queue state: %+v", got)
			}
			wantReply, err := json.Marshal(reply)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Close(data); err != nil {
				t.Fatal(err)
			}
			db, err = database.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			saved, done, err := state.For(db).RequestDone(t.Context(), "runner/"+rid)
			if err != nil || !done || !bytes.Equal(saved, wantReply) {
				t.Fatalf("claim receipt did not survive reopen: done=%v, %v", done, err)
			}
			replayed, after, err := queryQueueRow(db, info.ID)
			if err != nil || revision != after || !bytes.Equal(stored, replayed) {
				t.Fatalf("reopen changed claim state or revision: %q -> %q, %v", revision, after, err)
			}
		})
	}
}
