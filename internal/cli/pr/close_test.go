package pr

import (
	"bytes"
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

func TestClosePRUsesEngineAndPostsTypedOutcome(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "close-work"})
	if err != nil {
		t.Fatal(err)
	}
	s := watch.New()
	s.Notify = notify.New(rooms, subagent.NewManager())
	view := &prview.Service{Notify: s.Notify}
	s.Store = view.Watcher().Store
	engineCalls, err := websvc.Bind([]websvc.Binding{{Name: "ClosePR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.ClosePR}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), engineCalls)

	const key = "github.com/owner/repo#17"
	const url = "https://github.com/owner/repo/pull/17"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, RoomID: rm.ID, State: "open", Watching: true}}}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, s.Store.Root, data)

	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("CLOSE_TEST_CALLS", calls)
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte(`#!/bin/sh
[ "$1" = auth ] && exit 0
printf '%s\n' "$*" >> "$CLOSE_TEST_CALLS"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	s.GH.Rooms = rooms
	s.GH.Executable = gh
	roomfixture.Repository(t, rooms, rm.ID, "github.com", "owner/repo", "read-write")
	t.Setenv(ghclient.BinaryEnv, gh)

	cmd := Command(Options{Service: func() *watch.Service { return s }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"close", url, "--reason", "superseded by #18"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("close: %v %s", err, out.String())
	}

	log, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(log), "pr close -- "+url+"\n"; got != want {
		t.Fatalf("GitHub commands: %q, want %q", got, want)
	}
	saved, err := s.Store.Get(key)
	if err != nil || saved.State != "closed" || saved.Watching {
		t.Fatalf("closed record: %+v %v", saved, err)
	}
	messages, err := rooms.Messages(rm.ID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("room posts: %+v %v", messages, err)
	}
	event, ok := messages[0].PR()
	if !ok || event.Type != room.PRClosed || event.Number != 17 || !strings.Contains(event.Detail, "Reason: superseded by #18") {
		t.Fatalf("close event: %+v %v", event, ok)
	}
}
