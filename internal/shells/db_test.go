package shells

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func TestFirstOpenImportsLegacyJSONJobs(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	started := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	write := func(id, name string, value any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(s.Root, id), 0700); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteJSON(filepath.Join(s.Root, id, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	done, open := "sh_20261018-090000-aaaaaaaa", "sh_20261018-090001-bbbbbbbb"
	write(done, "meta.json", Meta{ID: done, Cmd: []string{"true"}, Cwd: "/", Owner: "ses_x", RoomID: "room", Started: started})
	write(done, "exit.json", Exit{Code: 3, Ended: started.Add(time.Minute)})
	write(open, "meta.json", Meta{ID: open, Cmd: []string{"sleep", "1"}, Cwd: "/", Started: started.Add(time.Second)})
	// An unpublished record never started a command.
	write("sh_20261018-090002-cccccccc", "meta.json.tmp", Meta{ID: "sh_20261018-090002-cccccccc"})

	list, err := s.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != open || list[1].ID != done {
		t.Fatalf("imported jobs = %#v", list)
	}
	if got := list[1]; got.Status != "exited" || got.Exit.Code != 3 || !got.Exit.Ended.Equal(started.Add(time.Minute)) || got.Owner != "ses_x" || got.RoomID != "room" {
		t.Fatalf("finished job = %#v", got)
	}
	if got := list[0]; got.Status != "lost" || got.Exit != nil || got.Cmd[0] != "sleep" {
		t.Fatalf("open job = %#v", got)
	}
}

func TestLegacyImportRefusesDamagedPublishedJobs(t *testing.T) {
	const id = "sh_20261018-090000-aaaaaaaa"
	for name, files := range map[string]map[string]string{
		"malformed meta":      {"meta.json": "{"},
		"wrong identity":      {"meta.json": `{"id":"sh_20261018-090000-bbbbbbbb","cmd":["true"]}`},
		"output without meta": {"output.log": "started"},
		"staging with a pid":  {"meta.json.tmp": `{"id":"` + id + `","pid":1}`},
		"malformed exit":      {"meta.json": `{"id":"` + id + `","cmd":["true"]}`, "exit.json": "{"},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
			if err := os.MkdirAll(filepath.Join(s.Root, id), 0700); err != nil {
				t.Fatal(err)
			}
			for file, text := range files {
				if err := os.WriteFile(filepath.Join(s.Root, id, file), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.List(true); err == nil || !strings.Contains(err.Error(), id) {
				t.Fatalf("List error = %v, want one naming %s", err, id)
			}
		})
	}
}

func TestCreateIsTheFirstOperationOnAFreshStore(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if in, err := s.Info(meta.ID); err != nil || in.ID != meta.ID {
		t.Fatalf("Info = %#v, %v", in, err)
	}
	if list, err := s.List(true); err != nil || len(list) != 1 {
		t.Fatalf("List = %#v, %v", list, err)
	}
}

func TestVersionOneStoreUpgradesAndKeepsJobs(t *testing.T) {
	data := t.TempDir()
	s := &Store{Root: filepath.Join(data, "shells")}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	// A version 1 store: the first table and one finished job.
	err = database.Migrate(db, "shells.version", 1, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(context.Background(), schema); err != nil {
			return err
		}
		return upsert(conn, Meta{ID: "sh_20261018-090000-aaaaaaaa", Cmd: []string{"true"}, Cwd: "/", Started: time.Now()}, &Exit{Code: 0, Ended: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimExit("sh_20261018-090000-aaaaaaaa"); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if ok, err := s.ClaimExit("sh_20261018-090000-aaaaaaaa"); err != nil || ok {
		t.Fatalf("second claim = %v, %v", ok, err)
	}
}

func TestVersionTwoStoreGainsTerminalColumnsThatSaveKeeps(t *testing.T) {
	data := t.TempDir()
	s := &Store{Root: filepath.Join(data, "shells")}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	const id = "sh_20261018-090000-aaaaaaaa"
	stale := Meta{ID: id, Cmd: []string{"true"}, Cwd: "/", Started: time.Now()}
	err = database.Migrate(db, "shells.version", 2, func(conn *sql.Conn, _ int) error {
		for _, q := range []string{schema, schemaV2} {
			if _, err := conn.ExecContext(context.Background(), q); err != nil {
				return err
			}
		}
		return upsert(conn, stale, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	if err := s.MarkBackground(id, at); err != nil {
		t.Fatal(err)
	}
	if err := s.markStopped(id, "agent"); err != nil {
		t.Fatal(err)
	}
	if err := s.markStopped(id, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.setOutputLines(id, 12); err != nil {
		t.Fatal(err)
	}
	// A supervisor saves the exit from the Meta it read at start. That must
	// not erase what the other writers recorded since.
	if err := s.Save(stale, &Exit{Code: 1, Ended: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	m, err := s.ReadMeta(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.BackgroundAt == nil || !m.BackgroundAt.Equal(at) || m.StopBy != "agent" || m.OutputLines != 12 {
		t.Fatalf("meta = %#v", m)
	}
}

// Another process holding dyna.db's write lock must delay a shell job's
// write, not fail it: a failed write loses the job's start or its exit.
func TestShellJobWriteSurvivesAHeldWriteLock(t *testing.T) {
	data := t.TempDir()
	s := &Store{Root: filepath.Join(data, "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+filepath.Join(data, "dyna.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() { saved <- s.Save(meta, &Exit{Code: 3, Ended: time.Now()}) }()
	select {
	case err := <-saved:
		t.Fatalf("the write did not wait for the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
		// A negative check: the write must still be waiting for the lock.
	}
	if _, err := conn.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatalf("write after the lock was released: %v", err)
	}
	if ex, err := s.ReadExit(meta.ID); err != nil || ex == nil || ex.Code != 3 {
		t.Fatalf("exit = %#v, %v", ex, err)
	}
}
