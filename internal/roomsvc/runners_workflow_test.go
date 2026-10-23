package roomsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
)

func workflowWatchFixture(t *testing.T) (*runnerWatch, *sql.DB, chan string, func()) {
	t.Helper()
	dir := storetest.DataDir(t)
	if err := callerid.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if err := runstore.Migrate(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	exited := make(chan string, 4)
	s := New(nil)
	s.CoordinatorExited = func(id string) { exited <- id }
	w := s.newRunners()
	t.Cleanup(func() {
		w.mu.Lock()
		closed := w.closed
		w.mu.Unlock()
		if !closed {
			closeCoordinatorWatch(t, w)
		}
	})
	cmd := exec.Command("/bin/cat")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			stdin.Close()
			_ = cmd.Wait()
		}
	}
	t.Cleanup(stop)
	body, err := json.Marshal(runstore.Meta{ID: "wf_watch", Pid: cmd.Process.Pid, Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO runs(id,session,started,body) VALUES(?,?,?,?)", "wf_watch", "", 0, body); err != nil {
		t.Fatal(err)
	}
	if err := callerid.RecordStarted(t.Context(), cmd.Process.Pid, callerid.Launch{DataDir: dir, Name: "wf_watch", Actor: callerid.Identity{Kind: callerid.Worker, Run: "wf_watch"}, Launcher: "workflow"}); err != nil {
		t.Fatal(err)
	}
	return w, db, exited, stop
}

func receiveCoordinatorExit(t *testing.T, exited <-chan string) {
	t.Helper()
	select {
	case id := <-exited:
		if id != "wf_watch" {
			t.Fatalf("exit owner=%q", id)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("coordinator exit did not recover its spool")
	}
}

func TestCoordinatorWatchSeedsTerminalCleanupAndDeduplicates(t *testing.T) {
	w, _, exited, stop := workflowWatchFixture(t)
	if err := w.followCoordinators(t.Context(), map[string]bool{"": true}); err != nil {
		t.Fatal(err)
	}
	if err := w.followCoordinators(t.Context(), map[string]bool{"wf_watch": true}); err != nil {
		t.Fatal(err)
	}
	if len(w.legacy) != 0 || len(w.unwatched) != 0 || len(w.watched) != 0 {
		t.Fatal("coordinator entered session watch/probe maps")
	}
	stop()
	receiveCoordinatorExit(t, exited)
	if err := w.followCoordinators(t.Context(), map[string]bool{"wf_watch": true}); err != nil {
		t.Fatal(err)
	}
	closeCoordinatorWatch(t, w)
	if len(exited) != 0 {
		t.Fatal("duplicate coordinator hint recovered its exit twice")
	}
}

func TestCoordinatorHintRecoversExitBeforePidfdOpen(t *testing.T) {
	w, _, exited, stop := workflowWatchFixture(t)
	stop()
	if err := w.followCoordinators(t.Context(), map[string]bool{"wf_watch": true}); err != nil {
		t.Fatal(err)
	}
	receiveCoordinatorExit(t, exited)
}

func TestCoordinatorWatchRefusesMismatchedRecords(t *testing.T) {
	for _, query := range []string{
		"UPDATE caller_records SET start=start+1",
		"UPDATE caller_records SET worker=1",
		"UPDATE caller_records SET run='wf_foreign'",
		"UPDATE caller_records SET actor='session'",
	} {
		t.Run(query, func(t *testing.T) {
			w, db, exited, stop := workflowWatchFixture(t)
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if err := w.followCoordinators(t.Context(), map[string]bool{"": true}); err != nil {
				t.Fatal(err)
			}
			stop()
			closeCoordinatorWatch(t, w)
			if len(exited) != 0 {
				t.Fatal("stale or mismatched record was treated as a coordinator")
			}
		})
	}
}

func TestCoordinatorWatchCloseJoinsLivePidfd(t *testing.T) {
	w, _, exited, _ := workflowWatchFixture(t)
	if err := w.followCoordinators(t.Context(), map[string]bool{"": true}); err != nil {
		t.Fatal(err)
	}
	closeCoordinatorWatch(t, w)
	if len(exited) != 0 {
		t.Fatal("closing the watcher reported a live coordinator exit")
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "record-workflow-coordinator" {
		pid, err := strconv.Atoi(os.Args[3])
		if err == nil {
			err = callerid.RecordStarted(context.Background(), pid, callerid.Launch{DataDir: os.Args[2], Name: "wf_watch", Actor: callerid.Identity{Kind: callerid.Worker, Run: "wf_watch"}, Launcher: "workflow"})
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCoordinatorHintFindsExternalRegistrationAfterStartup(t *testing.T) {
	w, db, exited, stop := workflowWatchFixture(t)
	var pid int
	if err := db.QueryRow("SELECT pid FROM caller_records LIMIT 1").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM caller_records"); err != nil {
		t.Fatal(err)
	}
	if err := w.followCoordinators(t.Context(), map[string]bool{"": true}); err != nil {
		t.Fatal(err)
	}
	var published atomic.Int64
	state.For(db).SetPublish(func(_ hub.Event) { published.Add(1) })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	phase, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	record := exec.CommandContext(phase, executable, "record-workflow-coordinator", w.dataDir, strconv.Itoa(pid))
	record.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	if raw, err := record.CombinedOutput(); err != nil {
		t.Fatalf("external record: %s %v", raw, err)
	}
	if published.Load() != 0 {
		t.Fatal("an external launcher reached the engine-local publish hook")
	}
	// Socket identification queues only a run hint; consume it in the normal
	// Watch select without a tick or database work in the identification callback.
	service := New(nil)
	service.CoordinatorChanged("wf_watch")
	service.CoordinatorChanged("wf_watch")
	waiting, finish := context.WithTimeout(t.Context(), 30*time.Second)
	defer finish()
	scans := 0
	if service.waitTick(waiting, nil, func() { t.Fatal("hint scanned PRs") }, func() { t.Fatal("hint scanned sessions") }, func() {
		scans++
		if err := w.followCoordinators(waiting, service.takeCoordinatorHints()); err != nil {
			t.Fatal(err)
		}
		finish()
	}, func() { t.Fatal("hint scanned call requests") }) {
		t.Fatal("coordinator hint required a clock tick")
	}
	if scans != 1 {
		t.Fatalf("duplicate hints caused %d scans", scans)
	}
	stop()
	receiveCoordinatorExit(t, exited)
}

func closeCoordinatorWatch(t *testing.T, w *runnerWatch) {
	t.Helper()
	done := make(chan struct{})
	go func() { w.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("coordinator watcher close did not join")
	}
}
