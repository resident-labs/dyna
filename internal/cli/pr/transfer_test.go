package pr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestPRTransferCommand(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
	work, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	// The target takes the transfer notice, so it can resume a turn.
	target := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000002-bbbbbbbb", Name: "next", RoomID: work.ID, Room: work.Name, Profile: "mock", Resumable: true, HarnessSession: "harness-next"}}
	s := watch.New()
	s.Notify = notify.New(rooms, subagent.NewManager())
	// transfer is an engine call: the fake engine serves it from a pr/view
	// service over this same PR store.
	view := &prview.Service{Notify: s.Notify, SessionInRoom: func(_, ref string) (subagent.Info, error) {
		if ref != target.ID {
			return subagent.Info{}, fmt.Errorf("no session %s", ref)
		}
		return target, nil
	}}
	s.Store = view.Watcher().Store
	engineCalls, err := websvc.Bind([]websvc.Binding{{Name: "TransferPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.TransferPR}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), engineCalls)
	recordtest.Write(t, filepath.Join(filepath.Dir(s.Store.Root), "sessions"), target.ID, target.Meta)
	const url = "https://github.com/owner/repo/pull/17"
	const key = "github.com/owner/repo#17"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, RoomID: work.ID, Author: "old-author", State: "open", Watching: true}}}
	b, _ := json.Marshal(st)
	saveState(t, s.Store.Root, b)
	seen := ""
	cmd := Command(Options{Service: func() *watch.Service { return s }, Session: func(ref string) (subagent.Info, error) { seen = ref; return target, nil }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"transfer", url, "--to", "@next"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if seen != "next" || out.String() != "PR #17 author transferred to work:next ("+target.ID+").\n" {
		t.Fatalf("transfer output: %s target=%q", out.String(), seen)
	}
	out.Reset()
	cmd.SetArgs([]string{"transfer", url, "--to", "@next", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var rec watch.Record
	if err := json.Unmarshal(out.Bytes(), &rec); err != nil || rec.Author != target.ID {
		t.Fatalf("JSON output = %s, error = %v", out.String(), err)
	}
}
