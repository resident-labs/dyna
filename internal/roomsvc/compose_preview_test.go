package roomsvc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestPreviewUsesRoutingEligibilityWithoutRepair(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	write := func(path string, v any) []byte {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		return b
	}
	metas := []subagent.Meta{
		{ID: "ses_20261017-120000-00000001", Name: "ready", HarnessSession: "mock-ready", Resumable: true, Turns: 1},
		{ID: "ses_20261017-120000-00000002", Name: "new", Resumable: true},
		{ID: "ses_20261017-120000-00000003", Name: "unsupported", HarnessSession: "mock-old"},
		{ID: "ses_20261017-120000-00000004", Name: "child", HarnessSession: "mock-child", Resumable: true, ParentSession: "ses_20261017-120000-00000001"},
	}
	for _, m := range metas {
		m.RoomID, m.Room = rec.ID, rec.Name
		recordtest.Write(t, e.mgr.Store.Root, m.ID, m)
	}
	path := filepath.Join(e.mgr.Store.Root, metas[0].ID, "turns", "1.json")
	before := write(path, subagent.Turn{N: 1, Status: subagent.TurnRunning})
	e.mgr.Store.ReportRepaired = func(*subagent.Store, string, int) { t.Error("preview reported a repair") }
	d, err := e.svc.Preview(rec.ID, "@team hello")
	if err != nil || !reflect.DeepEqual(d.Wakes, []string{"ready"}) {
		t.Fatalf("preview = %+v, %v", d, err)
	}
	route, err := e.svc.Notify.Preview(notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}, d.Mentions)
	if err != nil || len(route.Skipped) != 2 {
		t.Fatalf("routing preview = %+v, %v", route, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("preview changed orphaned turn: %s, %v", after, err)
	}
	for _, draft := range []string{"@new hello", "@unsupported hello", "@missing hello", "@parent hello"} {
		_, previewErr := e.svc.Preview(rec.ID, draft)
		_, postErr := e.svc.Post(e.ctx, rec.ID, draft, "")
		if previewErr == nil || postErr == nil || previewErr.Error() != postErr.Error() {
			t.Fatalf("%q: preview=%v post=%v", draft, previewErr, postErr)
		}
	}
	d, err = e.svc.Preview(rec.ID, "@root hello")
	if err != nil || !d.Self || !reflect.DeepEqual(d.Wakes, []string{"root"}) {
		t.Fatalf("owner preview = %+v, %v", d, err)
	}
}
