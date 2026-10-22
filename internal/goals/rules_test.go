package goals

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRulesLifecycle(t *testing.T) {
	s := testStore(t)
	empty, err := s.RulesView("room")
	if err != nil || empty.Rules == nil || empty.Used != 0 || empty.Max != 3000 {
		t.Fatal(empty, err)
	}
	first, err := s.AddRule("room", "owner", "owner", "  Keep\n things\t simple. ")
	if err != nil || first.ID != "r1" || first.Text != "Keep things simple." {
		t.Fatal(first, err)
	}
	before, _ := s.RulesView("room")
	for i := 2; i <= 12; i++ {
		rule, err := s.AddRule("room", "agent", "helper", fmt.Sprintf("Rule %d", i))
		if err != nil || rule.ID != fmt.Sprintf("r%d", i) {
			t.Fatal(rule, err)
		}
	}
	rules, _ := s.Rules("room")
	if rules[1].ID != "r2" || rules[11].ID != "r12" {
		t.Fatal(rules)
	}
	s.Now = func() time.Time { return first.CreatedAt.Add(time.Hour) }
	edited, err := s.EditRule("room", "r2", "other-agent", "other-agent", "Changed")
	if err != nil || edited.Setter != "agent" || edited.SetterName != "helper" || len(edited.Edits) != 1 || edited.Edits[0].OldText != "Rule 2" || edited.UpdatedAt.Equal(edited.CreatedAt) {
		t.Fatal(edited, err)
	}
	if _, err := s.EditRule("room", "r1", "agent", "agent", "Changed"); err == nil || err.Error() != "r1 was written by the owner. Ask the owner to change it." {
		t.Fatal(err)
	}
	if _, err := s.RemoveRule("room", "r1", "agent"); err == nil || err.Error() != "r1 was written by the owner. Ask the owner to change it." {
		t.Fatal(err)
	}
	if _, err := s.EditRule("room", "r1", "owner", "owner", "Changed owner rule"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveRule("room", "r2", "other-agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveRule("room", "r1", "owner"); err != nil {
		t.Fatal(err)
	}
	next, err := s.AddRule("room", "owner", "owner", "Next")
	if err != nil || next.ID != "r13" {
		t.Fatal(next, err)
	}
	after, err := (&Store{Root: s.Root}).RulesView("room")
	if err != nil || after.Hash == before.Hash || len(after.Rules) != 11 {
		t.Fatal(after, err)
	}
	again, _ := s.RulesView("room")
	if again.Hash != after.Hash {
		t.Fatal("unstable hash")
	}
	if _, err := s.EditRule("other", "r3", "owner", "owner", "text"); err == nil {
		t.Fatal("cross-room edit")
	}
	if _, err := s.RemoveRule("room", "missing", "owner"); err == nil {
		t.Fatal("missing rule")
	}
}

func TestRuleLimits(t *testing.T) {
	s := testStore(t)
	for _, tc := range []struct{ text, want string }{{" \n", "rule text is required"}, {strings.Repeat("界", 501), "rule text exceeds 500 characters"}} {
		if _, err := s.AddRule("room", "owner", "owner", tc.text); err == nil || err.Error() != tc.want {
			t.Fatal(err)
		}
	}
	if _, err := s.AddRule("", "owner", "owner", "text"); err == nil {
		t.Fatal("empty room")
	}
	for i := 0; i < 6; i++ {
		if _, err := s.AddRule("room", "owner", "owner", strings.Repeat("界", 500)); err != nil {
			t.Fatal(err)
		}
	}
	want := "Rules are full (3 000 / 3 000 characters). Shorten or remove a rule first."
	if _, err := s.AddRule("room", "owner", "owner", "x"); err == nil || err.Error() != want {
		t.Fatal(err)
	}
	if _, err := s.EditRule("room", "r1", "owner", "owner", strings.Repeat("x", 501)); err == nil || err.Error() != "rule text exceeds 500 characters" {
		t.Fatal(err)
	}
	if _, err := s.EditRule("room", "r1", "owner", "owner", " "); err == nil || err.Error() != "rule text is required" {
		t.Fatal(err)
	}
	if _, err := s.EditRule("room", "r1", "owner", "owner", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRule("room", "owner", "owner", strings.Repeat("y", 499)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditRule("room", "r1", "owner", "owner", "xx"); err == nil || err.Error() != want {
		t.Fatal(err)
	}
	for i := 0; i < MaxRules; i++ {
		if _, err := s.AddRule("count", "owner", "owner", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddRule("count", "owner", "owner", "x"); err == nil || err.Error() != "this scope has 20 rules. Shorten or remove a rule first." {
		t.Fatal(err)
	}
	if _, err := s.EditRule("count", "r1", "owner", "owner", "valid edit at cap"); err != nil {
		t.Fatal(err)
	}
}

func TestRuleCapUsesCurrentCharacterCount(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 6; i++ {
		n := 500
		if i == 5 {
			n = 480
		}
		if _, err := s.AddRule("room", "owner", "owner", strings.Repeat("界", n)); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.RulesView("room")
	if before.Used != 2980 {
		t.Fatal(before)
	}
	if _, err := s.AddRule("room", "owner", "owner", strings.Repeat("x", 21)); err == nil || err.Error() != "Rules are full (2 980 / 3 000 characters). Shorten or remove a rule first." {
		t.Fatal(err)
	}
	after, _ := s.RulesView("room")
	if before.Hash != after.Hash || after.Used != 2980 {
		t.Fatal("rejected add changed rules", after)
	}
	if _, err := s.AddRule("room", "owner", "owner", strings.Repeat(" ", 1000)+"folded"); err != nil {
		t.Fatal(err)
	}
}

func TestRulesShareStoreAndLock(t *testing.T) {
	s := testStore(t)
	if _, err := s.Add("room", "owner", Input{Text: "Goal", Shared: true}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AddRule("room", "owner", "owner", "Rule"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	rules, err := s.Rules("room")
	if err != nil || len(rules) != 20 {
		t.Fatal(rules, err)
	}
	state, err := s.Read()
	if err != nil || state.Version != 1 || len(state.Rooms["room"].Goals) != 1 {
		t.Fatal(state, err)
	}
}

func TestGlobalRulesAreSeparateFromRooms(t *testing.T) {
	s := testStore(t)
	if _, err := s.AddRule("room", "owner", "owner", "Room rule"); err != nil {
		t.Fatal(err)
	}
	global, err := s.AddRule(Global, "agent", "helper", "Use Central time.")
	if err != nil || global.ID != "u1" {
		t.Fatal(global, err)
	}
	room, _ := s.Rules("room")
	all, _ := s.Rules(Global)
	if len(room) != 1 || room[0].ID != "r1" || len(all) != 1 || all[0].Text != "Use Central time." {
		t.Fatal(room, all)
	}
	if _, err := s.EditRule("room", "u1", "owner", "owner", "x"); err == nil {
		t.Fatal("room scope edited a global rule")
	}
	if _, err := s.EditRule(Global, "u1", "agent", "agent", "Use UTC."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveRule(Global, "u1", "owner"); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.Rules(Global); len(left) != 0 {
		t.Fatal(left)
	}
}
