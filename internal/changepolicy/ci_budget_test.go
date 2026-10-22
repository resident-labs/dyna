package changepolicy

import (
	"testing"
	"time"
)

func TestCIBudget(t *testing.T) {
	w, f, e := (Policy{}).CIBudget()
	if e != nil || w != 2*time.Minute || f != 3*time.Minute {
		t.Fatalf("defaults %v %v %v", w, f, e)
	}
	for _, p := range []Policy{{CIWarnAfter: "0s"}, {CIFailAfter: "1m"}, {CIWarnAfter: "bad"}, {CIFailAfter: "bad"}} {
		if _, _, err := p.CIBudget(); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

func TestCIBudgetExemptNames(t *testing.T) {
	for _, names := range [][]string{{""}, {" CodeQL"}, {"CodeQL", "CodeQL"}, {"bad\nname"}} {
		if _, _, err := (Policy{CIBudgetExempt: names}).CIBudget(); err == nil {
			t.Fatalf("accepted %q", names)
		}
	}
	p := Policy{CIBudgetExempt: []string{"CodeQL"}}
	if p.CIBudgetApplies("CodeQL") || !p.CIBudgetApplies("Build") {
		t.Fatal("wrong exemption match")
	}
}

func TestTestBudget(t *testing.T) {
	max, allowed, err := (Policy{}).TestBudget()
	if err != nil || max != 10*time.Second || len(allowed) != 0 {
		t.Fatalf("defaults: %s %v %v", max, allowed, err)
	}
	for _, p := range []Policy{{CITestMax: "0s"}, {CITestMax: "bad"}, {CITestAllow: []string{"bad\nname"}}} {
		if _, _, err := p.TestBudget(); err == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	max, allowed, err = (Policy{CITestMax: "2s", CITestAllow: []string{"pkg/slow"}}).TestBudget()
	if err != nil || max != 2*time.Second || !allowed["pkg/slow"] {
		t.Fatalf("custom budget: %s %v %v", max, allowed, err)
	}
	disabled := false
	if !(Policy{}).WarnNoJUnit() || (Policy{CINoJUnitWarning: &disabled}).WarnNoJUnit() {
		t.Fatal("warning defaults")
	}
}
