package pr

import (
	"context"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/websvc"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/store/storetest"
)

func TestMergeFixUsesEngineCall(t *testing.T) {
	storetest.Isolate(t)
	const key = "github.com/owner/repo#1"
	s := watch.New()
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Kind: "github", RoomID: "repair-room", State: "open", Watching: true}}}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, s.Store.Root, raw)
	called := false
	sentinel := errors.New("engine handled merge-fix")
	calls, err := websvc.Bind([]websvc.Binding{{Name: "MergeFixPR", Access: websvc.Write, Scope: websvc.ScopeAdmin, Local: true, Method: func(ctx context.Context, room, got string) error {
		called = true
		if room != "repair-room" || got != key {
			t.Errorf("engine arguments = %q, %q", room, got)
		}
		return sentinel
	}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	cmd := Command(Options{Service: func() *watch.Service { return s }})
	cmd.SetArgs([]string{"merge-fix", "https://github.com/owner/repo/pull/1"})
	if err := cmd.Execute(); !called || err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("engine called=%v, error=%v", called, err)
	}
}
