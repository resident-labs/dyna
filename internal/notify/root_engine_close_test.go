package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// In the engine, a direct post to a root that cannot take posts replaces that
// root, whoever posted. A workflow worker may not close a session itself; the
// replacement is the engine's own maintenance, and the closed root's events
// name the engine, not the poster.
func TestDirectPostReplacesAnUnusableRootInTheEngine(t *testing.T) {
	for _, role := range []authority.Role{authority.Session, authority.Worker} {
		t.Run(string(role), func(t *testing.T) { directPostReplacesUnusableRoot(t, role) })
	}
}

func directPostReplacesUnusableRoot(t *testing.T, role authority.Role) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	agent := e.start(rec, "agent", nil)
	if got := rootDelivery(e.human(rec, "RESPOND: first answer", "@root"), rec); got != "root-started" {
		t.Fatalf("root delivery = %q, want root-started", got)
	}
	old := e.wait(e.rootSession(rec).ID, 1)
	// An idle root with no harness session to resume cannot take posts.
	if _, err := e.mgr.Store.UpdateMeta(old.ID, func(m *subagent.Meta) { m.HarnessSession = "" }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.InEngine())
	ctx := authority.WithActor(e.ctx, authority.Actor{Role: role, Session: agent.ID, Room: rec.ID})
	msg, err := e.svc.Rooms.Post(rec.ID, room.PostInput{ID: "msg_00000000000000aa", Wake: true, From: agent.Address(), To: []string{rec.Name + ":" + room.RootAgent}, Kind: "message", NeedsRoot: true, Text: "RESPOND: hi"})
	if err != nil {
		t.Fatal(err)
	}
	d, handled := e.svc.deliverRoot(ctx, msg, rec)
	if !handled || d.Delivery == "failed" {
		t.Fatalf("delivery = %+v, %v; want the root replaced", d, handled)
	}
	if now := e.rootSession(rec); now.ID == old.ID {
		t.Fatalf("root %s was not replaced", old.ID)
	}
	dir, err := e.mgr.Store.Dir(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	by := ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil && ev["t"] == "session_closed" {
			by, _ = ev["by"].(string)
		}
	}
	if !strings.HasPrefix(by, "engine: ") {
		t.Fatalf("closed root's session_closed by = %q; want the engine", by)
	}
}
