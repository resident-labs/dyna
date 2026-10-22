package terminalsvc_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/terminalsvc"
)

func TestTerminalDirectoryKeepsSessionDirectory(t *testing.T) {
	svc, rooms, sessions := roomTerminalService(t)
	repo := t.TempDir()
	if b, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, b)
	}
	rec, err := rooms.Create(room.CreateInput{Name: "repo", Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	roomID, sessionID, cwd, err := svc.TerminalDirectory(rec.ID)
	if err != nil || roomID != rec.ID || sessionID != "" || cwd != repo {
		t.Fatalf("room: %q %q %q %v", roomID, sessionID, cwd, err)
	}
	in := subagent.Meta{ID: subagent.NewID(), Name: "worker", RoomID: rec.ID, Room: rec.Name, Dir: t.TempDir()}
	recordtest.Write(t, sessions.Root, in.ID, in)
	roomID, sessionID, cwd, err = svc.TerminalDirectory(in.ID)
	if err != nil || roomID != rec.ID || sessionID != in.ID || cwd != in.Dir || cwd == repo {
		t.Fatalf("session: %q %q %q %v", roomID, sessionID, cwd, err)
	}
	empty, err := rooms.Create(room.CreateInput{Name: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.TerminalDirectory(empty.ID); err == nil {
		t.Fatal("room with no repo accepted")
	}
}

func TestShellJobsAndBoundedPlainLog(t *testing.T) {
	svc, rooms, sessions := roomTerminalService(t)
	rec, err := rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rooms.Create(room.CreateInput{Name: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	store := terminalsvc.ShellStore(sessions.Root)
	first, err := store.Create(shells.StartOptions{Cmd: []string{"make", "test"}, Cwd: t.TempDir(), RoomID: rec.ID, Owner: "alpha:worker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: other.ID}); err != nil {
		t.Fatal(err)
	}
	path, err := store.OutputPath(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("first\n\x1b[31mred\x1b[0m\nlast\n"), 0600); err != nil {
		t.Fatal(err)
	}
	jobs, err := svc.ShellJobs(rec.ID, "", 0)
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].ID != first.ID || jobs.Items[0].Owner != first.Owner || strings.Join(jobs.Items[0].Command, " ") != "make test" {
		t.Fatalf("jobs: %+v %v", jobs, err)
	}
	log, err := svc.ShellLog(first.ID, 2)
	if err != nil || log.Text != "red\nlast" || !log.Truncated {
		t.Fatalf("log: %+v %v", log, err)
	}
	if _, err := svc.ShellLog(filepath.Join("..", "outside"), 10); err == nil {
		t.Fatal("invalid shell id accepted")
	}
}

func TestStopShellStopsOnlyTheRoomsShellForTheOwner(t *testing.T) {
	svc, rooms, sessions := roomTerminalService(t)
	rec, err := rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rooms.Create(room.CreateInput{Name: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	store := terminalsvc.ShellStore(sessions.Root)
	job, err := store.Create(shells.StartOptions{Cmd: []string{"/bin/true"}, Cwd: t.TempDir(), RoomID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	// A completed shell takes Stop's path without timing a process start.
	// Supervise ran in this test process, so clear its supervisor identity.
	if err := store.Supervise(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	meta, err := store.ReadMeta(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta.Supervisor = 0
	exit, err := store.ReadExit(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(meta, exit); err != nil {
		t.Fatal(err)
	}
	owner := authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
	session := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: "ses_20261020-120000-abcdef01"})
	if _, err := svc.StopShell(session, rec.ID, job.ID); err == nil || !strings.Contains(err.Error(), "dyna shell stop") {
		t.Fatalf("a session stopped a shell through the engine: %v", err)
	}
	if _, err := svc.StopShell(owner, other.ID, job.ID); err == nil || !strings.Contains(err.Error(), "not in room beta") {
		t.Fatalf("another room's shell was stopped: %v", err)
	}
	row, err := svc.StopShell(owner, rec.ID, job.ID)
	if err != nil || row.ID != job.ID || row.State != "exited" {
		t.Fatalf("stop: %+v %v", row, err)
	}
}

func roomTerminalService(t *testing.T) (*terminalsvc.Service, *room.Store, *subagent.Store) {
	t.Helper()
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	sessions := &subagent.Store{Root: filepath.Join(root, "sessions")}
	svc := terminalsvc.New(terminalsvc.Options{
		Rooms: rooms, SessionRoot: sessions.Root,
		SessionDirectory: func(ref string) (string, string, string, error) {
			meta, err := sessions.ReadMeta(ref)
			return meta.RoomID, meta.ID, meta.Dir, err
		},
	})
	t.Cleanup(svc.Shutdown)
	return svc, rooms, sessions
}

func TestShellLogRangeReadsForwardByLines(t *testing.T) {
	svc, rooms, sessions := roomTerminalService(t)
	rec, err := rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	store := terminalsvc.ShellStore(sessions.Root)
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.OutputPath(job.ID)
	if err := os.WriteFile(path, []byte("alpha\r\n\x1b[31mred\x1b[0m\nprogress 10%\rprogress 100%\r\nlast"), 0600); err != nil {
		t.Fatal(err)
	}
	// The limit ends mid-line, so the chunk stops at the last whole line.
	first, err := svc.ShellLogRange(job.ID, 0, 10)
	if err != nil || first.Text != "alpha\r\n" || first.Next != 7 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	rest, err := svc.ShellLogRange(job.ID, first.Next, 0)
	if err != nil || rest.Text != "\x1b[31mred\x1b[0m\nprogress 10%\rprogress 100%\r\nlast" || rest.Next != rest.Size {
		t.Fatalf("rest = %+v, %v", rest, err)
	}
	if end, err := svc.ShellLogRange(job.ID, rest.Size+99, 0); err != nil || end.Text != "" || end.Next != rest.Size+99 {
		t.Fatalf("past the end = %+v, %v", end, err)
	}
	if _, err := svc.ShellLogRange(filepath.Join("..", "outside"), 0, 0); err == nil {
		t.Fatal("invalid shell id accepted")
	}
}

func TestShellLogRangeKeepsCharactersWholeAcrossChunks(t *testing.T) {
	_, rooms, sessions := roomTerminalService(t)
	rec, err := rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	store := terminalsvc.ShellStore(sessions.Root)
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.OutputPath(job.ID)
	// One long line with no line end, and 2, 3 and 4 byte characters in it.
	want := strings.Repeat("a", 10) + "é€😀" + strings.Repeat("b", 5) + strings.Repeat("😀", 40)
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	for limit := 1; limit <= len(want); limit++ {
		var got strings.Builder
		var offset int64
		for steps := 0; offset < int64(len(want)); steps++ {
			chunk, err := terminalsvc.ReadLogChunk(path, offset, limit)
			if err != nil || steps > len(want) || chunk.Next <= offset {
				t.Fatalf("limit %d: chunk %+v at %d, %v", limit, chunk, offset, err)
			}
			// A chunk ends within 3 bytes of the limit: just enough to finish a character.
			if got := chunk.Next - offset; got > int64(limit)+3 {
				t.Fatalf("limit %d: chunk of %d bytes", limit, got)
			}
			if !utf8.ValidString(chunk.Text) {
				t.Fatalf("limit %d: chunk %q is not valid UTF-8", limit, chunk.Text)
			}
			got.WriteString(chunk.Text)
			offset = chunk.Next
		}
		if got.String() != want {
			t.Fatalf("limit %d: chunks join to %q, want %q", limit, got.String(), want)
		}
	}
}

func TestShellLogRangeChunksJoinToTheRawLog(t *testing.T) {
	_, rooms, sessions := roomTerminalService(t)
	rec, err := rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	store := terminalsvc.ShellStore(sessions.Root)
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.OutputPath(job.ID)
	whole := "alpha\r\n\x1b[31;1mred\x1b[0m \x1b]0;title\x07ok\x1b(Bhi\x1bPq data\x1b\\\nprogress 10%\rprogress 100%\r\n" + "\x1b]0;" + strings.Repeat("x", 300) + "\x07" + strings.Repeat("é€😀", 20) + "\x1b[2K\x1b"
	if err := os.WriteFile(path, []byte(whole), 0600); err != nil {
		t.Fatal(err)
	}
	want := whole
	for limit := 1; limit <= len(whole); limit++ {
		var got strings.Builder
		var offset int64
		for steps := 0; offset < int64(len(whole)); steps++ {
			chunk, err := terminalsvc.ReadLogChunk(path, offset, limit)
			if err != nil || steps > len(whole) || chunk.Next <= offset {
				t.Fatalf("limit %d: chunk %+v at %d, %v", limit, chunk, offset, err)
			}
			if got := chunk.Next - offset; got > int64(limit)+3 {
				t.Fatalf("limit %d: chunk of %d bytes", limit, got)
			}
			got.WriteString(chunk.Text)
			offset = chunk.Next
		}
		if got.String() != want {
			t.Fatalf("limit %d: chunks join to %q, want %q", limit, got.String(), want)
		}
	}
}

func TestShellLogRangeBoundsAChunkForOutputThatIsNotUTF8(t *testing.T) {
	_, rooms, sessions := roomTerminalService(t)
	rec, err := rooms.Create(room.CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	store := terminalsvc.ShellStore(sessions.Root)
	job, err := store.Create(shells.StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir(), RoomID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := store.OutputPath(job.ID)
	// Continuation bytes with no lead byte: never a whole character.
	if err := os.WriteFile(path, []byte(strings.Repeat("\x80", 5000)), 0600); err != nil {
		t.Fatal(err)
	}
	var offset int64
	for steps := 0; offset < 5000; steps++ {
		chunk, err := terminalsvc.ReadLogChunk(path, offset, 1)
		if err != nil || steps > 5000 || chunk.Next <= offset || chunk.Next-offset > 1+3 {
			t.Fatalf("chunk %+v at %d, %v", chunk, offset, err)
		}
		offset = chunk.Next
	}
}
