package pr

// This file times the hold command end to end through the real engine
// handler chain, against PR tables copied out of a real dyna.db. It is
// skipped unless PR_BENCH_SOURCE names a copied source dyna.db; CI never
// sets it. The engine handler (room routing, authorization, record read,
// gh call, room post) runs in this process over a fresh isolated data
// folder; a fake gh binary answers the draft call. Between runs an
// unrelated row is written from a second connection, so the shared
// snapshot is stale the way a poll worker leaves it. Limits: no engine
// background workers, no real GitHub, no production rooms or sessions;
// the notice posts to one copied room record with the owner as sender.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestHoldThroughEngineBenchmark(t *testing.T) {
	source := os.Getenv("PR_BENCH_SOURCE")
	if source == "" {
		t.Skip("set PR_BENCH_SOURCE to a copied dyna.db holding the production PR tables")
	}
	data := storetest.DataDir(t)
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	// The isolated store owns its schema; the source database only lends
	// its rows, so each run stays isolated from it.
	if err := prstate.Migrate(data); err != nil {
		t.Fatal(err)
	}
	if err := room.Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	copyTable := func(name, cols string) {
		t.Helper()
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(context.Background(), fmt.Sprintf("ATTACH DATABASE 'file:%s?mode=ro' AS src", source)); err != nil {
			t.Fatalf("attach source database: %v", err)
		}
		if _, err := conn.ExecContext(context.Background(), "INSERT INTO main."+name+"("+cols+") SELECT "+cols+" FROM src."+name); err != nil {
			t.Fatalf("copy %s: %v", name, err)
		}
		if _, err := conn.ExecContext(context.Background(), "DETACH DATABASE src"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	copyTable("pr_state", "id, revision, body")
	copyTable("pr_rows", "key, room, repo, number, closed, rev, body")
	copyTable("pr_record_parts", "key, part, body")
	var key, roomID, staleKey string
	err = db.QueryRow(`SELECT key, json_extract(body,'$.record.roomId') FROM pr_rows
 WHERE closed=0 AND json_extract(body,'$.record.state')='open'
   AND COALESCE(json_extract(body,'$.record.watching'),0)=1
   AND COALESCE(json_extract(body,'$.record.kind'),'github')='github' LIMIT 1`).Scan(&key, &roomID)
	if err != nil {
		t.Fatalf("pick a holdable PR: %v", err)
	}
	if err := db.QueryRow("SELECT min(key) FROM pr_rows WHERE key<>?", key).Scan(&staleKey); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store.DataDir(), "rooms", roomID), 0o700); err != nil {
		t.Fatal(err)
	}

	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_GH_BINARY", gh)

	rooms := roomsvc.New(nil)
	copyTable("room_records", "id, rev, body")
	bound, err := websvc.Bind([]websvc.Binding{{Name: "SetPRDraft", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: rooms.PRs.SetPRDraft}})
	if err != nil {
		t.Fatal(err)
	}
	serveBenchEngine(t, data, bound)

	s := watch.New()
	s.Store = &watch.Store{Root: prstate.Root(data)}
	run := func() time.Duration {
		cmd := Command(Options{Service: func() *watch.Service { return s }})
		cmd.SetArgs([]string{"hold", prURL(key), "benchmark"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		start := time.Now()
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	stale := func() {
		// database.Open shares one cached handle for the folder; closing it
		// here would close the engine side too.
		bump, err := database.Open(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bump.Exec("UPDATE pr_state SET revision=revision+1 WHERE id=1"); err != nil {
			t.Fatal(err)
		}
		if _, err := bump.Exec("UPDATE pr_rows SET rev=(SELECT revision FROM pr_state WHERE id=1) WHERE key=?", staleKey); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("hold cold total: %v (key %s, room %s)", run(), key, roomID)
	for i := 0; i < 6; i++ {
		stale()
		t.Logf("hold stale-snapshot total: %v", run())
	}
}
