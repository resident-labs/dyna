package view_test

import (
	"dyna-agent/internal/mode"
	prview "dyna-agent/internal/pr/view"
	"testing"
)

func TestSetRoomCIBudgetResolvesRoomAndScopesChanges(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("budget-alpha"), e.room("budget-beta")
	// Web mutations act as the owner, independent of the engine's stamp.
	t.Setenv(mode.EnvMode, mode.Workflow)
	t.Setenv(mode.EnvSession, "ses_engine_worker")
	t.Setenv(mode.EnvRoomID, beta.ID)
	p := prview.CIBudgetSettings{CITimeBudget: "warn", CIWarnAfter: "4m", CIFailAfter: "5m", CITestMax: "20s", CITestAllow: []string{"pkg/slow"}, CITestAllowNotes: map[string]string{"pkg/slow": "12 seconds: setup"}}
	if err := e.svc.PRs.SetRoomCIBudget(ownerCtx, alpha.Name, p); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.PRs.RoomChangePolicy(alpha.ID)
	if err != nil || got.CITestMax != "20s" || got.CITestAllowNotes["pkg/slow"] != "12 seconds: setup" {
		t.Fatalf("room budget: %+v, %v", got, err)
	}
	other, err := e.svc.PRs.RoomChangePolicy(beta.ID)
	if err != nil || other.CITestMax != "10s" {
		t.Fatalf("other room changed: %+v, %v", other, err)
	}
	if err := e.svc.PRs.SetRoomCIBudget(ownerCtx, "missing", p); err == nil {
		t.Fatal("missing room accepted")
	}
}
