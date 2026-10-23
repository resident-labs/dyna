package view_test

import (
	"dyna-agent/internal/mode"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"testing"
)

func TestChangePolicyAPIRoundTripAndRoomBoundary(t *testing.T) {
	e := newEnv(t)
	alpha, beta := e.room("policy-alpha"), e.room("policy-beta")
	// Web mutations act as the owner, independent of the engine's stamp.
	t.Setenv(mode.EnvMode, mode.Workflow)
	t.Setenv(mode.EnvSession, "ses_engine_worker")
	t.Setenv(mode.EnvRoomID, beta.ID)
	p, err := e.svc.PRs.RoomChangePolicy(alpha.ID)
	if err != nil || p.Review != "on" {
		t.Fatalf("defaults %+v %v", p, err)
	}
	p.RequiredChecks = []string{"unit"}
	if err = e.svc.PRs.SetRoomChangePolicy(ownerCtx, alpha.ID, p); err != nil {
		t.Fatal(err)
	}
	p2, err := e.svc.PRs.RoomChangePolicy(alpha.ID)
	if err != nil || len(p2.RequiredChecks) != 1 {
		t.Fatalf("round trip %+v %v", p2, err)
	}
	st, _ := e.svc.PRs.Watcher().Store.Read()
	st.Records["change"] = &watch.Record{Key: "change", Kind: "local", RoomID: alpha.ID, State: "open", Watching: true, Head: "head"}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	o := watch.PolicyOverride{Review: "off", Reason: "Owner reviewed directly"}
	if err = e.svc.PRs.SetChangePolicyOverride(ownerCtx, beta.ID, "change", o); err == nil {
		t.Fatal("override crossed room boundary")
	}

	if err = e.svc.PRs.SetChangePolicyOverride(ownerCtx, alpha.ID, "change", o); err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.PRs.Changes(alpha.ID, "", 0)
	if err != nil || len(rows.Items) != 1 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	detail, err := e.svc.PRs.ChangeDetail(alpha.ID, "change")
	if err != nil || detail.Policy.Review != "off" || len(detail.SkipReasons) != 2 || detail.PolicyOverride.Reason != o.Reason {
		t.Fatalf("policy detail %+v %v", detail, err)
	}
}
