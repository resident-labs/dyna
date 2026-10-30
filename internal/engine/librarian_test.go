package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/librarian"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func librarianRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "fixture"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "entry.go"), []byte("package entry\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestLibrarianSharedHooks(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	repo := librarianRepo(t)
	calls, _, err := callsFor(s, false)
	if err != nil {
		t.Fatal(err)
	}
	// A recursive call fails immediately. The correct path uses the same
	// hook implementations directly, without taking the engine gate again.
	for _, name := range []string{"ListHooks", "AddHook", "RemoveHook"} {
		calls[name] = func(context.Context, []json.RawMessage) (any, error) { return nil, errors.New("recursive hook call") }
	}
	enginetest.Serve(t, store.DataDir(), calls)
	client := engineclient.New("")
	// Each call keeps the engine's own per-call deadline; one short bound
	// over the whole flow failed under CI load (rule r7). go test's timeout
	// still ends a hang.
	ctx := t.Context()
	var enabled librarian.Enabled
	if err := client.Call(ctx, "EnableLibrarian", []any{repo, "", repo}, &enabled); err != nil {
		t.Fatal(err)
	}
	hooks, err := s.hooks.List(ctx, repo)
	if err != nil || len(hooks) != 1 || hooks[0].Name != "librarian-main" {
		t.Fatalf("hooks = %+v, %v", hooks, err)
	}
	for _, hook := range hooks {
		if hook.AddedBy != "owner" {
			t.Fatalf("hook audit = %+v", hook)
		}
	}
	var issue room.Issue
	sm := librarian.Smell{Title: "One shared entry path", At: []string{"entry.go:1"}, Symbols: []string{"entry"}, Cost: "Repeated edits.", Direction: "Share the entry."}
	if err := client.Call(ctx, "FileLibrarySmell", []any{repo, sm}, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.Filer != enabled.Room {
		t.Fatalf("issue filer = %q, want %q", issue.Filer, enabled.Room)
	}
	if err := client.Call(ctx, "DisableLibrarian", []any{repo}, nil); err != nil {
		t.Fatal(err)
	}
	hooks, err = s.hooks.List(ctx, repo)
	if err != nil || len(hooks) != 0 {
		t.Fatalf("hooks left = %+v, %v", hooks, err)
	}
	var result librarian.Result
	if err := client.Call(ctx, "WakeLibrarian", []any{repo}, &result); err != nil || result.Action != "off" {
		t.Fatalf("wake = %+v, %v", result, err)
	}
}

// TestLibrarianRestartKeepsBatchedWake starts an engine whose last librarian run
// is now, with main moved. The wake must defer to the interval's end after a
// restart, because the deferred timer does not survive one.
func TestLibrarianRestartKeepsBatchedWake(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	repo := librarianRepo(t)
	owner := localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
	enabled, err := s.enableLibrarian(owner, repo, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	states := librarianstate.Store{DataDir: store.DataDir()}
	if _, err := states.Update(enabled.Repo.ID, false, func(st *librarianstate.State) error {
		st.LastRun = now
		st.Head = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The fake clock fires nothing by itself. The test calls each recorded deadline.
	var fired []func()
	after := func(_ time.Duration, fn func()) func() bool {
		fired = append(fired, fn)
		return func() bool { return true }
	}
	s.timers = newTimers(t.Context(), func() time.Time { return now }, after)
	s.restoreLibrarianWakes(t.Context())
	if len(fired) != 1 {
		t.Fatalf("restore scheduled %d wakes, want 1", len(fired))
	}
	fired[0]()
	at, err := s.librarianDeferred(t.Context(), repo)
	if err != nil || at == nil || !at.Equal(now.Add(librarian.Interval)) {
		t.Fatalf("deferred = %v, %v", at, err)
	}
}

func TestLibrarianEngineCannotUseProcessAuthority(t *testing.T) {
	storetest.Isolate(t)
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner})
	ctx = callspec.WithClaim(ctx, callspec.Claim{Session: "other"})
	s := services{}
	if _, err := s.enableLibrarian(ctx, "missing", "", "missing"); err == nil || !strings.Contains(err.Error(), "could not verify") {
		t.Fatalf("enable = %v", err)
	}
	// Disable reaches the same explicit service guard. Give it the real service
	// dependencies; its actor must still come from the call, not the process.
	s = testServices(t, false)
	if _, err := s.disableLibrarian(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "could not verify") {
		t.Fatalf("disable = %v", err)
	}
	if _, err := s.fileLibrarySmell(ctx, librarianRepo(t), librarian.Smell{}); err == nil || !strings.Contains(err.Error(), "could not verify") {
		t.Fatalf("smell = %v", err)
	}
}

func TestLibrarianEngineKeepsCallerRoom(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	own, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "own"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.rooms.Notify.Rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: "root"}, authority.Actor{Role: authority.Root, Session: "root", Room: own.ID})
	if _, err := s.enableLibrarian(ctx, "missing", other.ID, "missing"); err == nil || !strings.Contains(err.Error(), "cannot switch") {
		t.Fatalf("room switch = %v", err)
	}
}

func TestLibrarianSmellRefusesUnverifiedAuthor(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	repo := librarianRepo(t)
	owner := localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
	enabled, err := s.enableLibrarian(owner, repo, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	// An unresolved caller cannot file an issue with the engine owner actor.
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Unverified}, authority.Actor{Role: authority.Owner})
	sm := librarian.Smell{Title: "One shared entry path", At: []string{"entry.go"}, Symbols: []string{"entry"}, Cost: "Repeated edits.", Direction: "Share the entry."}
	issue, err := s.fileLibrarySmell(ctx, repo, sm)
	if err == nil || !strings.Contains(err.Error(), "could not verify") || issue.ID != "" {
		t.Fatalf("issue = %+v, %v", issue, err)
	}
	st, err := (librarianstate.Store{DataDir: store.DataDir()}).Get(enabled.Repo.ID)
	if err != nil || st.Smells != 0 {
		t.Fatalf("state = %+v, %v", st, err)
	}
}

func TestLibrarianSmellUsesVerifiedAddress(t *testing.T) {
	storetest.Isolate(t)
	s := testServices(t, false)
	repo := librarianRepo(t)
	owner := localCallerContext(t, websvc.Caller{Kind: websvc.Owner}, authority.Actor{Role: authority.Owner})
	enabled, err := s.enableLibrarian(owner, repo, "", repo)
	if err != nil {
		t.Fatal(err)
	}
	st, err := (librarianstate.Store{DataDir: store.DataDir()}).Get(enabled.Repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := "ses_20261020-000001-aaaaaaaa"
	recordtest.Write(t, s.rooms.Notify.Sessions.Store.Root, id, subagent.Meta{ID: id, Name: "librarian", RoomID: st.RoomID, Room: enabled.Room, Role: subagent.RoleLibrarian})
	if _, err := (librarianstate.Store{DataDir: store.DataDir()}).Update(enabled.Repo.ID, false, func(st *librarianstate.State) error { st.SmellSession = id; return nil }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_ADDRESS", "forged:address")
	ctx := localCallerContext(t, websvc.Caller{Kind: websvc.Session, Session: id}, authority.Actor{Role: authority.Session, Session: id, Room: st.RoomID})
	sm := librarian.Smell{Title: "One shared entry path", At: []string{"entry.go"}, Symbols: []string{"entry"}, Cost: "Repeated edits.", Direction: "Share the entry."}
	issue, err := s.fileLibrarySmell(ctx, repo, sm)
	if err != nil || issue.Filer != enabled.Room+":librarian" {
		t.Fatalf("issue = %+v, %v", issue, err)
	}
}
