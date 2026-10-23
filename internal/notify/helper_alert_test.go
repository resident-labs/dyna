package notify

import (
	"strings"
	"testing"

	"dyna-agent/internal/subagent"
)

// A helper has no role of its own, so its failure alert names the service that
// started it and offers a command that works. A helper whose service cannot be
// read stays a helper and offers no command.
func TestHelperFailureAlertNamesItsService(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	service := markService(t, e, e.start(rec, "librarian-service", nil), subagent.RoleLibrarian)
	helper := e.start(rec, "helper", &service)
	if _, err := e.mgr.Store.UpdateTurn(helper.ID, 1, func(turn *subagent.Turn) {
		turn.Status, turn.Error = subagent.TurnError, "helper tool failed"
	}); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Message.Text
	if !strings.HasPrefix(text, "Service "+subagent.RoleLibrarian+" failed turn 1: helper tool failed") {
		t.Fatalf("helper alert names no service: %q", text)
	}
	if !strings.Contains(text, "dyna services log "+subagent.RoleLibrarian) {
		t.Fatalf("helper alert offers no usable command: %q", text)
	}

	// A parent that cannot be read leaves the helper without a command, not
	// with one that fails.
	orphan := e.start(rec, "orphan", &service)
	if _, err := e.mgr.Store.UpdateMeta(orphan.ID, func(meta *subagent.Meta) { meta.ParentSession = "ses_missing" }); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Store.UpdateTurn(orphan.ID, 1, func(turn *subagent.Turn) {
		turn.Status, turn.Error = subagent.TurnError, "orphan failed"
	}); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.CompleteSessionTurn(e.ctx, orphan.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Message.Text; !strings.HasPrefix(text, "Service helper failed turn 1:") || strings.Contains(text, "dyna services") {
		t.Fatalf("orphan helper alert = %q", text)
	}
}
