package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestRequireSendsReviewersToEngineWithoutLocalWrites(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "require-work"})
	if err != nil {
		t.Fatal(err)
	}
	s := watch.New()
	s.Notify = notify.New(rooms, subagent.NewManager())
	const key = "github.com/owner/repo#21"
	const url = "https://github.com/owner/repo/pull/21"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 21, URL: url, RoomID: rm.ID, State: "open", Watching: true}}}
	b, _ := json.Marshal(st)
	saveState(t, s.Store.Root, b)
	s.GH.Executable = "/must-not-contact-github"

	// The engine receives reviewer strings as given; it resolves them, not the CLI.
	var engineErr error
	var calls int
	var gotRoom, gotKey string
	var gotReviewers []string
	bound, err := websvc.Bind([]websvc.Binding{{Name: "RequirePRReviewers", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, roomRef, key string, reviewers []string) error {
		calls++
		gotRoom, gotKey, gotReviewers = roomRef, key, reviewers
		return engineErr
	}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), bound)
	run := func(args ...string) error {
		cmd := Command(Options{Service: func() *watch.Service { return s }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"require"}, args...))
		return cmd.Execute()
	}
	before, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}

	if err := run(url, "alice", "bob-review"); err != nil {
		t.Fatalf("require: %v", err)
	}
	if calls != 1 || gotRoom != rm.ID || gotKey != key || !reflect.DeepEqual(gotReviewers, []string{"alice", "bob-review"}) {
		t.Fatalf("engine got calls=%d room=%q key=%q reviewers=%q", calls, gotRoom, gotKey, gotReviewers)
	}

	// An engine refusal reaches the caller unchanged.
	engineErr = errors.New("reviewer ghost is not in this room")
	if err := run(url, "ghost"); err == nil || !strings.Contains(err.Error(), engineErr.Error()) {
		t.Fatalf("engine refusal: %v, want %q", err, engineErr)
	}

	// At least one reviewer is required, so no engine call is made.
	if err := run(url); err == nil {
		t.Fatal("accepted a PR with no reviewers")
	}
	if calls != 2 {
		t.Fatalf("engine calls = %d, want 2 (a call without reviewers reached the engine)", calls)
	}

	after, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("require changed local PR state:\nbefore %+v\nafter  %+v", before, after)
	}
}
