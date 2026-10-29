package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestHoldAndReleaseUseGitHubDraftAndPostReason(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "hold-work"})
	if err != nil {
		t.Fatal(err)
	}
	s := watch.New()
	s.Notify = notify.New(rooms, subagent.NewManager())
	// hold and release are engine calls: the fake engine serves them from a
	// pr/view service over this same PR store.
	view := &prview.Service{Notify: s.Notify}
	s.Store = view.Watcher().Store
	engineCalls, err := websvc.Bind([]websvc.Binding{
		{Name: "SetPRDraft", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.SetPRDraft},
		// gh pr ready checks the hold through the engine, as the engine serves it.
		{Name: "PRHold", Access: websvc.Read, Scope: websvc.ScopeRead, Method: view.PRHold},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), engineCalls)
	const key = "github.com/owner/repo#17"
	const url = "https://github.com/owner/repo/pull/17"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, RoomID: rm.ID, State: "open", Watching: true}}}
	b, _ := json.Marshal(st)
	saveState(t, s.Store.Root, b)
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("HOLD_TEST_CALLS", calls)
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\n[ \"$1\" = auth ] && exit 0\nprintf '%s\\n' \"$*\" >> \"$HOLD_TEST_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s.GH.Rooms = rooms
	roomfixture.Repository(t, rooms, rm.ID, "github.com", "owner/repo", "read-write")
	s.GH.Executable = gh
	// The engine's own watcher, which holds and releases, uses it too.
	t.Setenv(ghclient.BinaryEnv, gh)
	for _, args := range [][]string{{"hold", url, "waiting for the shared facts fix"}, {"release", url}} {
		cmd := Command(Options{Service: func() *watch.Service { return s }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out.String())
		}
	}
	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(log), "pr ready --undo -- "+url+"\npr ready -- "+url+"\n"; got != want {
		t.Fatalf("GitHub commands: %q, want %q", got, want)
	}
	messages, err := rooms.Messages(rm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || !strings.Contains(messages[0].Text, "waiting for the shared facts fix") || !strings.Contains(messages[1].Text, "released") {
		t.Fatalf("room posts: %+v", messages)
	}
	// pr/view checks who may hold (TestTransferAndDraftActForTheirCaller).
	if err := s.SetDraft(context.Background(), key, true, "  "); err == nil {
		t.Fatal("accepted a hold without a reason")
	}
	if got, err := os.ReadFile(calls); err != nil || string(got) != string(log) {
		t.Fatalf("invalid holds contacted GitHub: %q %v", got, err)
	}
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDraft(context.Background(), key, false, ""); err == nil {
		t.Fatal("reported release after GitHub failed")
	}
	messages, err = rooms.Messages(rm.ID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("failed request created a room post: %+v %v", messages, err)
	}
	saved, err := s.Store.Get(key)
	if err != nil || saved.State != "open" || !saved.Watching {
		t.Fatalf("tracking changed: %+v %v", saved, err)
	}
}
