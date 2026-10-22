package view_test

import (
	"context"
	"dyna-agent/internal/authority"
	prview "dyna-agent/internal/pr/view"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/websvc"
)

func TestRoomCIBudgetConversion(t *testing.T) {
	e := newEnv(t)
	a := e.svc.PRs
	rec, err := e.svc.Notify.Rooms.Create(room.CreateInput{Name: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.RoomCIBudget(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]time.Duration{got.CIWarnAfter: 2 * time.Minute, got.CIFailAfter: 3 * time.Minute, got.CITestMax: 10 * time.Second} {
		if d, err := time.ParseDuration(value); err != nil || d != want {
			t.Fatalf("default duration %q: %v", value, err)
		}
	}
	if got.CINoJUnitWarning == nil || !*got.CINoJUnitWarning || len(got.CITestAllow) != 0 {
		t.Fatalf("defaults: %+v", got)
	}
	base, err := a.RoomChangePolicy(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	base.CITestAllowNotes = map[string]string{"pkg.Test/a,b": "Calls an external service"}
	base.RequiredChecks, base.Land = []string{"special"}, "branch-only"
	if err = a.SetRoomChangePolicy(ownerCtx, rec.ID, base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"service"} {
		t.Run(name, func(t *testing.T) {
			calls, err := websvc.Bind([]websvc.Binding{{Name: "RoomCIBudget", Access: websvc.Read, Scope: websvc.ScopeRead, Method: a.RoomCIBudget}, {Name: "SetRoomCIBudget", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: a.SetRoomCIBudget}})
			if err != nil {
				t.Fatal(err)
			}
			warning := false
			want := prview.CIBudgetSettings{CITimeBudget: "warn", CIBudgetExempt: []string{"Weekly audit"}, CIWarnAfter: "4m0s", CIFailAfter: "5m0s", CITestMax: "20s", CITestAllow: []string{"pkg.Test/a,b", "suite.test[param value]"}, CITestAllowNotes: map[string]string{"pkg.Test/a,b": "12 seconds: external service", "suite.test[param value]": "15 seconds: integration setup"}, CINoJUnitWarning: &warning}
			arg, _ := json.Marshal(rec.ID)
			request := want
			request.CITestAllowNotes = map[string]string{"pkg.Test/a,b": "12 seconds: external service", "suite.test[param value]": "15 seconds: integration setup", "removed/test": "Old note"}
			budget, _ := json.Marshal(request)
			if _, err = calls["SetRoomCIBudget"](context.Background(), []json.RawMessage{arg, budget}); err != nil {
				t.Fatal(err)
			}
			out, err := calls["RoomCIBudget"](context.Background(), []json.RawMessage{arg})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(out, want) {
				t.Fatalf("budget roundtrip: %#v", out)
			}
			p, err := a.RoomChangePolicy(rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			p.CIBudgetExempt = base.CIBudgetExempt
			p.CITestAllowNotes = base.CITestAllowNotes
			p.CIWarnAfter, p.CIFailAfter, p.CITestMax, p.CITestAllow, p.CINoJUnitWarning = base.CIWarnAfter, base.CIFailAfter, base.CITestMax, base.CITestAllow, base.CINoJUnitWarning
			if !reflect.DeepEqual(p, base) {
				t.Fatalf("other policy settings changed: %#v", p)
			}
		})
	}
	// Invalid writes must preserve the last saved settings.
	before, _ := a.RoomCIBudget(rec.ID)
	for _, bad := range []prview.CIBudgetSettings{
		{CIWarnAfter: "5m", CIFailAfter: "4m"}, {CITestMax: "0s"}, {CITestMax: "bad"}, {CITestAllow: []string{" trailing "}},
	} {
		if err := a.SetRoomCIBudget(ownerCtx, rec.ID, bad); err == nil {
			t.Fatalf("accepted invalid budget: %#v", bad)
		}
		after, _ := a.RoomCIBudget(rec.ID)
		if !reflect.DeepEqual(after, before) {
			t.Fatal("invalid write changed policy")
		}
	}
	if _, err := a.RoomCIBudget("missing-room"); err == nil {
		t.Fatal("accepted missing room")
	}
	if err := a.SetRoomCIBudget(ownerCtx, "missing-room", prview.CIBudgetSettings{}); err == nil {
		t.Fatal("wrote missing room")
	}
	// The existing policy service retains the owner authorization gate.
	svc := &watch.Service{Store: watch.DefaultStore()}
	if err := svc.SetRoomChangePolicy(rec.ID, authority.Actor{Role: authority.Session, Session: "agent", Room: rec.ID}, base); err == nil {
		t.Fatal("agent bypassed owner gate")
	}
	if err := a.SetRoomCIBudget(ownerCtx, rec.ID, prview.CIBudgetSettings{}); err != nil {
		t.Fatal(err)
	}
	reset, err := a.RoomCIBudget(rec.ID)
	if err != nil || reset.CINoJUnitWarning == nil || !*reset.CINoJUnitWarning {
		t.Fatalf("reset warning default: %+v, %v", reset, err)
	}
}
