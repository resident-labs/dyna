package subagent

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestPromoteAndCloseChildrenCLI(t *testing.T) {
	storetest.Isolate(t)
	m := subagent.NewManager()
	work := workRoom(t, m)
	lead := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "lead", Room: work.Name, RoomID: work.ID, Dir: t.TempDir(), Depth: 1, Created: time.Now()}
	child := lead
	child.ID, child.Name, child.Depth, child.ParentSession, child.Owner = "ses_20261017-000002-bbbbbbbb", "child", 2, lead.ID, lead.ID
	grandchild := child
	grandchild.ID, grandchild.Name, grandchild.Depth, grandchild.ParentSession = "ses_20261017-000003-cccccccc", "grandchild", 3, child.ID
	for _, meta := range []subagent.Meta{lead, child, grandchild} {
		recordtest.Write(t, m.Store.Root, meta.ID, meta)
	}
	serveSessionControls(t, m)
	call := func(args ...string) (string, error) {
		cmd := newCommand(func() *subagent.Manager { return m })
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if _, err := call("adopt", grandchild.ID); err == nil {
		t.Fatal("adopt ran without --under")
	}
	if out, err := call("adopt", grandchild.ID, "--under", lead.ID); err != nil || !strings.Contains(out, "moved work:grandchild") || !strings.Contains(out, "depth 2") {
		t.Fatalf("adopt: %s %v", out, err)
	}
	if out, err := call("promote", grandchild.ID, "--json"); err != nil || !strings.Contains(out, `"depth": 1`) {
		t.Fatalf("promote JSON: %s %v", out, err)
	}
	if out, err := call("promote", grandchild.ID); err != nil || !strings.Contains(out, "top-level lead") {
		t.Fatalf("promote text: %s %v", out, err)
	}
	if out, err := call("close", lead.ID); err != nil || !strings.Contains(out, "closed work:lead") {
		t.Fatalf("close: %s %v", out, err)
	}
	// The child owns nothing, so it closes with its lead.
	if current, _ := m.Store.ReadMeta(child.ID); current.Closed.IsZero() {
		t.Fatalf("child = %+v", current)
	}
	if _, err := call("promote", lead.ID); err == nil {
		t.Fatal("promoted a closed lead")
	}
}
