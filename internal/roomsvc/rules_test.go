package roomsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRulesAPIAndEvents(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	other := e.room("other")
	initial, err := e.svc.Rules(r.Name, false)
	if err != nil || initial.Rules == nil || initial.Max != 3000 {
		t.Fatal(initial, err)
	}
	version := ""
	work := map[string]string{}
	var events []Event
	emit := func(ev Event) { events = append(events, ev) }
	goalRows := map[string]string{}
	e.svc.scanGoals(goalRows, work, &version, true, emit)
	rule, err := e.svc.AddRule(t.Context(), r.Name, false, "Keep things simple")
	if err != nil || rule.Setter != "owner" || rule.SetterName != "owner" {
		t.Fatal(rule, err)
	}
	e.svc.scanGoals(goalRows, work, &version, false, emit)
	if len(events) != 1 || events[0].Kind != "goals" || events[0].RoomID != r.ID {
		t.Fatal(events)
	}
	view, err := e.svc.Rules(r.ID, false)
	if err != nil || view.Used != 18 || len(view.Rules) != 1 || view.Hash == initial.Hash {
		t.Fatal(view, err)
	}
	if _, err := e.svc.EditRule(t.Context(), other.ID, false, rule.ID, "Wrong room"); err == nil {
		t.Fatal("cross-room edit")
	}
	if _, err := e.svc.RemoveRule(t.Context(), other.ID, false, rule.ID); err == nil {
		t.Fatal("cross-room removal")
	}
	edited, err := e.svc.EditRule(t.Context(), r.ID, false, rule.ID, "Keep it simple")
	if err != nil || len(edited.Edits) != 1 {
		t.Fatal(edited, err)
	}
	if _, err := e.svc.AddRule(t.Context(), r.ID, false, strings.Repeat("x", 501)); err == nil {
		t.Fatal("accepted long rule")
	}
	if _, err := e.svc.RemoveRule(t.Context(), r.Name, false, rule.ID); err != nil {
		t.Fatal(err)
	}
	msgs, err := e.svc.rooms().Messages(r.ID)
	if err != nil || len(msgs) != 3 {
		t.Fatal(msgs, err)
	}
	for _, msg := range msgs {
		if msg.Kind != "notice" || msg.From != "owner" || fmt.Sprint(msg.To) != "[work]" {
			t.Fatal(msg)
		}
	}
	if msgs[0].Text != "owner added rule r1: Keep things simple" || msgs[2].Text != "owner removed rule r1." {
		t.Fatal(msgs)
	}
	for _, ref := range []string{"missing", ""} {
		if _, err := e.svc.Rules(ref, false); err == nil {
			t.Fatal("unknown room")
		}
		if _, err := e.svc.AddRule(t.Context(), ref, false, "Rule"); err == nil {
			t.Fatal("unknown room")
		}
		if _, err := e.svc.EditRule(t.Context(), ref, false, "r1", "Rule"); err == nil {
			t.Fatal("unknown room")
		}
		if _, err := e.svc.RemoveRule(t.Context(), ref, false, "r1"); err == nil {
			t.Fatal("unknown room")
		}
	}
}

func TestRulesAPIReturnsSavedRuleWhenNoticeFails(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	feed := filepath.Join(e.svc.rooms().Root, r.ID, "messages.jsonl")
	if err := os.Remove(feed); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(feed, 0700); err != nil {
		t.Fatal(err)
	}
	rule, err := e.svc.AddRule(t.Context(), r.ID, false, "First rule")
	if err != nil || rule.ID != "r1" || rule.Text != "First rule" {
		t.Fatal(rule, err)
	}
	view, err := e.svc.Rules(r.ID, false)
	if err != nil || len(view.Rules) != 1 || view.Rules[0].Text != rule.Text {
		t.Fatal(view, err)
	}
	rule, err = e.svc.EditRule(t.Context(), r.ID, false, rule.ID, "Changed rule")
	if err != nil || rule.ID != "r1" || rule.Text != "Changed rule" {
		t.Fatal(rule, err)
	}
	view, err = e.svc.Rules(r.ID, false)
	if err != nil || len(view.Rules) != 1 || view.Rules[0].Text != rule.Text {
		t.Fatal(view, err)
	}
	rule, err = e.svc.RemoveRule(t.Context(), r.ID, false, rule.ID)
	if err != nil || rule.ID != "r1" || rule.Text != "Changed rule" {
		t.Fatal(rule, err)
	}
	view, err = e.svc.Rules(r.ID, false)
	if err != nil || len(view.Rules) != 0 {
		t.Fatal(view, err)
	}
}

func TestGlobalRulesAPIUsesOneScopeForEveryRoom(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	other := e.room("other")
	rule, err := e.svc.AddRule(t.Context(), r.Name, true, "Use Central time")
	if err != nil || rule.ID != "u1" {
		t.Fatal(rule, err)
	}
	if view, err := e.svc.Rules(other.Name, true); err != nil || len(view.Rules) != 1 || view.Rules[0].ID != "u1" {
		t.Fatal("another room does not see the global rule:", view, err)
	}
	if view, err := e.svc.Rules(r.Name, false); err != nil || len(view.Rules) != 0 {
		t.Fatal("a global rule showed as a room rule:", view, err)
	}
	if _, err := e.svc.EditRule(t.Context(), other.Name, true, "u1", "Use UTC"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RemoveRule(t.Context(), r.Name, false, "u1"); err == nil {
		t.Fatal("room scope removed a global rule")
	}
	if _, err := e.svc.RemoveRule(t.Context(), r.Name, true, "u1"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := e.svc.rooms().Messages(r.ID)
	if len(msgs) != 2 || msgs[0].Text != "owner added global rule u1: Use Central time" || msgs[1].Text != "owner removed global rule u1." {
		t.Fatal(msgs)
	}
	if msgs, _ := e.svc.rooms().Messages(other.ID); len(msgs) != 1 || msgs[0].Text != "owner edited global rule u1: Use UTC" {
		t.Fatal(msgs)
	}
}
