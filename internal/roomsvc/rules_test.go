package roomsvc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
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
		if msg.Kind != "rule" || msg.Notice.Kind != "changed" || msg.From != room.EngineSender || fmt.Sprint(msg.To) != "[work]" {
			t.Fatal(msg)
		}
		if _, ok := msg.Rule(); !ok {
			t.Fatal("rule post has no payload", msg)
		}
	}
	added, _ := msgs[0].Rule()
	removed, _ := msgs[2].Rule()
	if added.Actor != "owner" || added.Verb != room.RuleAdded || added.Scope != "room" || removed.Verb != room.RuleRemoved {
		t.Fatal(msgs)
	}
	if msgs[0].Text != "owner added the room rule r1" || msgs[2].Text != "owner removed the room rule r1" {
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

// A shared edit has one owner row, while every session keeps its own agent
// note in its DM. A rule edit uses its existing rule row; another shared edit
// uses one instruction-batch row.
func TestSharedInstructionChangesProduceOneOwnerRow(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	ids := map[string]string{}
	for _, name := range []string{"one", "two", "three"} {
		info := e.start(r, name, nil)
		ids[info.Address()] = info.ID
	}
	sameSessions := func(got []string) bool {
		if len(got) != len(ids) {
			return false
		}
		seen := map[string]bool{}
		for _, address := range got {
			if ids[address] == "" || seen[address] {
				return false
			}
			seen[address] = true
		}
		return true
	}
	checkDMNotes := func(msgs []room.Message, want int) {
		t.Helper()
		notes := 0
		for _, msg := range msgs {
			ev, ok := msg.InstructionNote()
			if !ok {
				continue
			}
			notes++
			if want := room.DMConversation(ids[ev.Session]); msg.Conversation != want {
				t.Fatalf("instruction note for %s has home %q, want %q", ev.Session, msg.Conversation, want)
			}
		}
		if notes != want {
			t.Fatalf("instruction notes = %d, want %d", notes, want)
		}
	}
	flush := func() {
		t.Helper()
		// Exercise the earliest and latest queued deadlines without sleeping.
		// A shared edit must still produce one complete batch.
		var earliest, latest time.Time
		for _, id := range ids {
			meta, err := e.mgr.Store.Info(id)
			if err != nil {
				t.Fatal(err)
			}
			at := meta.InstructionsChangedAt
			if at.IsZero() {
				continue
			}
			at = at.Add(3 * time.Second)
			if earliest.IsZero() || at.Before(earliest) {
				earliest = at
			}
			if latest.IsZero() || at.After(latest) {
				latest = at
			}
		}
		next, err := e.mgr.FlushInstructionNotes(e.ctx, earliest)
		if err != nil {
			t.Fatal(err)
		}
		if !next.IsZero() {
			if _, err := e.mgr.FlushInstructionNotes(e.ctx, latest); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := e.svc.AddRule(e.ctx, r.ID, false, "Keep every shared notice together"); err != nil {
		t.Fatal(err)
	}
	flush()
	msgs, err := e.svc.rooms().Messages(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkDMNotes(msgs, 3)
	var rules []room.RuleEvent
	for _, msg := range msgs {
		if ev, ok := msg.Rule(); ok {
			rules = append(rules, ev)
		}
	}
	if len(rules) != 1 || !sameSessions(rules[0].Sessions) {
		t.Fatalf("rule reach = %+v, want one row for %v", rules, ids)
	}

	// A different shared change uses one batch row rather than a rule row.
	e.mgr.Profiles = func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{
			{Name: "mock", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 5},
			{Name: "writer", Harness: profile.HarnessMock, Model: "model-2", Taste: 7, Intelligence: 8, Cost: 6},
		}}, nil
	}
	e.mgr.NoteRoomInstructions(e.ctx, r.ID)
	flush()
	msgs, err = e.svc.rooms().Messages(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkDMNotes(msgs, 6)
	var batches []room.InstructionBatchEvent
	ownerRows := 0
	for _, msg := range msgs {
		if ev, ok := msg.InstructionBatch(); ok {
			batches = append(batches, ev)
		}
		if msg.Conversation.Home() == room.ConversationRoom {
			if _, ok := msg.Rule(); ok {
				ownerRows++
			}
			if _, ok := msg.InstructionBatch(); ok {
				ownerRows++
			}
			if _, ok := msg.InstructionNote(); ok {
				ownerRows++
			}
		}
	}
	if len(batches) != 1 || len(batches[0].Sections) != 1 || batches[0].Sections[0].Name != "Subagent profiles" ||
		!sameSessions(batches[0].Sessions) || len(batches[0].Notes) != 3 {
		t.Fatalf("instruction batches = %+v", batches)
	}
	if ownerRows != 2 {
		t.Fatalf("owner rows = %d, want one rule row and one batch row", ownerRows)
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
	if len(msgs) != 2 {
		t.Fatal(msgs)
	}
	added, _ := msgs[0].Rule()
	removed, _ := msgs[1].Rule()
	if added.Actor != "owner" || added.Scope != "global" || added.Text != "Use Central time" || removed.Verb != room.RuleRemoved {
		t.Fatal(msgs)
	}
	elsewhere, _ := e.svc.rooms().Messages(other.ID)
	if len(elsewhere) != 1 {
		t.Fatal(elsewhere)
	}
	if edited, _ := elsewhere[0].Rule(); edited.Verb != room.RuleEdited || edited.Actor != "owner" {
		t.Fatal(elsewhere)
	}
}
