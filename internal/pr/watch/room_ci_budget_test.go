package watch

import (
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/pr/state"
)

func TestSetRoomCIBudgetUpdatesLivePolicyAndNotes(t *testing.T) {
	e := newEnv(t)
	id := e.author.RoomID
	p := changepolicy.Default()
	p.Review = "off"
	p.CITestAllow = []string{"legacy", "removed"}
	p.CITestAllowNotes = map[string]string{"removed": "old debt"}
	if err := e.s.SetRoomChangePolicy(id, authority.Actor{Role: authority.Owner}, p); err != nil {
		t.Fatal(err)
	}
	// Hold the store lock while a policy edit completes. The budget writer must
	// use the policy committed under that lock, not a previous read.
	held, release, edited := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		edited <- e.s.Store.update(func(st *State) error {
			close(held)
			<-release
			live := st.Policies[id]
			live.RequiredChecks = []string{"new-check"}
			st.Policies[id] = live
			return nil
		})
	}()
	<-held
	budget := ChangePolicy{CITimeBudget: "warn", CIWarnAfter: "4m", CIFailAfter: "5m", CITestMax: "20s", CITestAllow: []string{"legacy", "pkg/new"}, CITestAllowNotes: map[string]string{"pkg/new": " 12 seconds: service setup ", "removed": "old debt"}}
	saved := make(chan error, 1)
	go func() { saved <- e.s.SetRoomCIBudget(id, authority.Actor{Role: authority.Owner}, budget) }()
	close(release)
	if err := <-edited; err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	got, err := e.s.RoomChangePolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Review != p.Review || !reflect.DeepEqual(got.RequiredChecks, []string{"new-check"}) {
		t.Fatalf("lost policy edit: %+v", got)
	}
	if !reflect.DeepEqual(got.CITestAllowNotes, map[string]string{"pkg/new": "12 seconds: service setup"}) {
		t.Fatalf("notes not pruned: %+v", got.CITestAllowNotes)
	}
	budget.CITestAllowNotes["pkg/new"] = "mutated"
	again, _ := e.s.RoomChangePolicy(id)
	if again.CITestAllowNotes["pkg/new"] != "12 seconds: service setup" {
		t.Fatal("input aliases stored notes")
	}
	if err := e.s.SetRoomCIBudget(id, authority.Actor{Role: authority.Session, Session: "agent"}, budget); err == nil {
		t.Fatal("agent changed budget")
	}
	if err := e.s.SetRoomCIBudget("", authority.Actor{Role: authority.Owner}, budget); err == nil {
		t.Fatal("missing room accepted")
	}
	before := again
	for _, bad := range []ChangePolicy{{CITimeBudget: "warn", CIWarnAfter: "5m", CIFailAfter: "4m"}, {CITimeBudget: "warn", CITestMax: "0s"}, {CITimeBudget: "warn", CITestAllow: []string{"new-without-note"}}} {
		if err := e.s.SetRoomCIBudget(id, authority.Actor{Role: authority.Owner}, bad); err == nil {
			t.Fatalf("invalid budget accepted: %+v", bad)
		}
		after, _ := e.s.RoomChangePolicy(id)
		if !reflect.DeepEqual(after, before) {
			t.Fatal("invalid budget changed policy")
		}
	}
	r := e.track()
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].Landing = &state.Landing{State: "landing", At: e.clock}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetRoomCIBudget(id, authority.Actor{Role: authority.Owner}, budget); err != nil {
		t.Fatalf("saved landing history blocked a budget change: %v", err)
	}
}

func TestRoomBudgetReplacementClearsExemptions(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	id := "room"
	original := changepolicy.Default()
	original.CITimeBudget = "enforce"
	original.CIBudgetExempt = []string{"Weekly audit"}
	original.Review = "off"
	original.RequiredChecks = []string{"special"}
	if err := s.Store.update(func(st *State) error {
		st.Policies = map[string]ChangePolicy{id: original}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	budget := ChangePolicy{CITimeBudget: "warn"}
	if err := s.SetRoomCIBudget(id, authority.Actor{Role: authority.Owner}, budget); err != nil {
		t.Fatal(err)
	}
	got, err := s.RoomChangePolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CIBudgetExempt) != 0 || got.CITimeBudget != "warn" ||
		got.Review != original.Review ||
		!reflect.DeepEqual(got.RequiredChecks, original.RequiredChecks) {
		t.Errorf("budget replacement lost or retained fields: %+v", got)
	}
	if err := s.SetRoomCIBudget(id, authority.Actor{Role: authority.Owner}, ChangePolicy{}); err == nil {
		t.Error("empty mode accepted")
	}
	again, err := s.RoomChangePolicy(id)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("invalid write changed policy: %+v %v", again, err)
	}
}

// The service must use the supplied actor even in an owner or Root process.
func TestSetRoomCIBudgetRefusesActorBeforeStore(t *testing.T) {
	for _, processMode := range []string{"", "root", "subagent", "workflow"} {
		t.Run(processMode, func(t *testing.T) {
			t.Setenv("DYNA_MODE", processMode)
			s := &Service{}
			if err := s.SetRoomCIBudget("", authority.Actor{Role: authority.Owner}, ChangePolicy{}); err == nil || err.Error() != "room is required" {
				t.Fatalf("empty actor must reach room validation, got %v", err)
			}
			err := s.SetRoomCIBudget("room", authority.Actor{Role: authority.Session, Session: "session-actor"}, ChangePolicy{})
			if err == nil || !strings.HasPrefix(err.Error(), "SetRoomCIBudget is owner or root only; ") {
				t.Fatalf("want formatted authority refusal, got %v", err)
			}
		})
	}
}
