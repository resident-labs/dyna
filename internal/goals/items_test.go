package goals

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func openGoalDB(s *Store) (*sql.DB, error) { return database.Open(filepath.Dir(s.Root)) }

func itemStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	if _, err := s.Add("r", "owner", Input{Text: "Ship the relay", Shared: true}); err != nil {
		t.Fatal(err)
	}
	return s
}

// The item table is its own schema key, so it exists after the store opens.
func TestGoalItemsTable(t *testing.T) {
	s := itemStore(t)
	db, err := openGoalDB(s)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='goal_items'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("goal_items table missing")
	}
}

func TestAddItemListOrder(t *testing.T) {
	s := itemStore(t)
	first, err := s.AddItem("r", "g1", "owner", "First\nwith a brief", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "i1" || first.State != ItemOpen || first.Text != "First\nwith a brief" {
		t.Fatal(first)
	}
	if first.UpdatedAt.IsZero() {
		t.Fatal("item has no UpdatedAt")
	}
	for i := 2; i <= 11; i++ {
		if _, err := s.AddItem("r", "g1", "owner", strings.Repeat("x", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.Items("r", "g1")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "i1,i2,i3,i4,i5,i6,i7,i8,i9,i10,i11" {
		t.Fatalf("order: %v", ids)
	}
	if _, err := s.AddItem("r", "g99", "owner", "no goal", ""); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing goal: %v", err)
	}
	if _, err := s.AddItem("r", "g1", "owner", " ", ""); err == nil {
		t.Fatal("accepted empty text")
	}
	if _, err := s.AddItem("r", "g1", "owner", strings.Repeat("界", 16001), ""); err == nil {
		t.Fatal("accepted over-long text")
	}
	if _, err := s.AddItem("r", "g1", "owner", "text", "ses_20261021-120000-000000000000"); err == nil {
		t.Fatal("accepted an unknown session assignee")
	}
	if _, err := s.Finish("r", "g1", "owner", "done", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddItem("r", "g1", "owner", "late", ""); err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("closed goal: %v", err)
	}
}

func TestItemStatesAndEdit(t *testing.T) {
	s := itemStore(t)
	if _, err := s.AddItem("r", "g1", "owner", "task", ""); err != nil {
		t.Fatal(err)
	}
	done, err := s.SetItemState("r", "g1", "i1", "owner", ItemDone)
	if err != nil || done.State != ItemDone {
		t.Fatal(done, err)
	}
	if _, err := s.SetItemState("r", "g1", "i1", "owner", "bogus"); err == nil {
		t.Fatal("accepted an invalid state")
	}
	if _, err := s.SetItemState("r", "g1", "i9", "owner", ItemDone); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing item: %v", err)
	}
	// A done item keeps its assignee for the record.
	assigned, err := s.EditItem("r", "g1", "i1", "owner", "task two", nil)
	if err != nil || assigned.Text != "task two" {
		t.Fatal(assigned, err)
	}
	set := "owner"
	if _, err := s.EditItem("r", "g1", "i1", "owner", "", &set); err != nil {
		t.Fatal(err)
	}
	clear := ""
	back, err := s.EditItem("r", "g1", "i1", "owner", "", &clear)
	if err != nil || back.Assignee != "" {
		t.Fatal(back, err)
	}
	// Reopen returns the item to open with its text intact.
	open, err := s.SetItemState("r", "g1", "i1", "owner", ItemOpen)
	if err != nil || open.State != ItemOpen || open.Text != "task two" {
		t.Fatal(open, err)
	}
	items, err := s.Items("r", "g1")
	if err != nil || len(items) != 1 || items[0].State != ItemOpen || items[0].Assignee != "" {
		t.Fatal(items, err)
	}
	if _, err := s.EditItem("r", "g1", "i1", "owner", "", nil); err == nil || !strings.Contains(err.Error(), "supply") {
		t.Fatalf("empty edit: %v", err)
	}
}

func TestItemCounts(t *testing.T) {
	s := itemStore(t)
	if _, err := s.Add("r", "owner", Input{Text: "Another"}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct {
		goal  string
		texts []string
	}{{"g1", []string{"a", "b", "c"}}, {"g2", []string{"d"}}} {
		for _, text := range spec.texts {
			if _, err := s.AddItem("r", spec.goal, "owner", text, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.SetItemState("r", "g1", "i1", "owner", ItemDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemState("r", "g1", "i2", "owner", ItemDropped); err != nil {
		t.Fatal(err)
	}
	counts, err := s.ItemCounts("r")
	if err != nil {
		t.Fatal(err)
	}
	if got := counts["g1"]; got != [2]int{1, 3} {
		t.Fatalf("g1 counts: %v", got)
	}
	if got := counts["g2"]; got != [2]int{1, 1} {
		t.Fatalf("g2 counts: %v", got)
	}
}

// Item changes and notes append events to the journal file; keeper entries
// and events share the file without appearing in each other's reads.
func TestItemEvents(t *testing.T) {
	s := itemStore(t)
	if _, err := s.AddItem("r", "g1", "owner", "task", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournal("r", JournalEntry{Round: "rd1", At: time.Now(), Entries: []JournalLine{{Agent: "a", Status: "on_track", Summary: "s", Goals: []string{"g1"}}}}); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events("r", "g1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Goal != "g1" || events[0].Item != "i1" || events[0].By != "owner" || events[0].Event != "added" {
		t.Fatalf("events: %+v", events)
	}
	note, err := s.AddEvent("r", Event{Goal: "g1", By: "owner", Event: "note", Text: "state changed"})
	if err != nil {
		t.Fatal(err)
	}
	if note.ID == "" {
		t.Fatal("event has no ID")
	}
	events, err = s.Events("r", "g1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Event != "note" || events[0].Item != "" || events[0].Text != "state changed" || events[1].Event != "added" {
		t.Fatalf("events after note: %+v", events)
	}
	entries, _, err := s.Journal("r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Round != "rd1" {
		t.Fatalf("entries: %+v", entries)
	}
	// A raw event line in the file stays out of entry reads even before any
	// store method wrote it.
	path := filepath.Join(s.Root, "journal", "r.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"event","goal":"g1","item":"i1","by":"owner","event":"done","at":"2026-10-17T12:00:00Z"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	entries, _, err = s.Journal("r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("event line leaked into entries: %+v", entries)
	}
	events, err = s.Events("r", "g1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].ID == events[1].ID || events[0].Event != "done" {
		t.Fatalf("events after raw line: %+v", events)
	}
}

func TestEffectiveGoal(t *testing.T) {
	s := itemStore(t)
	if _, err := s.Add("r", "worker", Input{Text: "Self-set work", Holders: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.EffectiveGoal("r", "worker", ""); err != nil || got != "" {
		t.Fatalf("self-set goal became an implicit parent: %q %v", got, err)
	}
	g2, err := s.Add("r", "owner", Input{Text: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.EffectiveGoal("r", "owner", "g1"); err != nil || got != "g1" {
		t.Fatalf("explicit: %q %v", got, err)
	}
	// An assigned item wins over held goals.
	if _, err := s.AddItem("r", g2.ID, "owner", "delegated", "worker"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.EffectiveGoal("r", "worker", ""); err != nil || got != g2.ID {
		t.Fatalf("assigned item: %q %v", got, err)
	}
	// A single held goal for the setter.
	if got, err := s.EffectiveGoal("r", "setter", ""); err != nil || got != "" {
		t.Fatalf("no match: %q %v", got, err)
	}
	held, err := s.Add("r", "owner", Input{Text: "Held", Holders: []string{"worker"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemState("r", g2.ID, "i1", "owner", ItemDone); err != nil {
		t.Fatal(err)
	}
	if got, err := s.EffectiveGoal("r", "worker", ""); err != nil || got != held.ID {
		t.Fatalf("held after item done: %q %v", got, err)
	}
	if _, err := s.AddItem("r", held.ID, "owner", "another assignment", "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddItem("r", g2.ID, "owner", "second assignment", "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EffectiveGoal("r", "worker", ""); err == nil || !strings.Contains(err.Error(), "spans goals") {
		t.Fatalf("ambiguous assigned goals: %v", err)
	}
	if _, err := s.Finish("r", g2.ID, "owner", "Done", false); err != nil {
		t.Fatal(err)
	}
	if got, err := s.EffectiveGoal("r", "worker", ""); err != nil || got != held.ID {
		t.Fatalf("closed assigned goal must be ignored: %q %v", got, err)
	}
	if _, err := s.SetItemState("r", held.ID, "i1", "owner", ItemDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("r", "owner", Input{Text: "Two", Holders: []string{"worker"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EffectiveGoal("r", "worker", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous: %v", err)
	}
}

func TestReturnAndMoveItems(t *testing.T) {
	s := itemStore(t)
	g2, err := s.Add("r", "owner", Input{Text: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ goal, text string }{{"g1", "one"}, {"g1", "two"}, {g2.ID, "three"}} {
		if _, err := s.AddItem("r", spec.goal, "owner", spec.text, "worker"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetItemState("r", "g1", "i1", "owner", ItemDone); err != nil {
		t.Fatal(err)
	}
	returned, err := s.ReturnItems("r", "worker")
	if err != nil || returned != 2 {
		t.Fatalf("returned = %d err=%v", returned, err)
	}
	first, err := s.Items("r", "g1")
	if err != nil || len(first) != 2 || first[0].State != ItemDone || first[0].Assignee != "worker" || first[1].Assignee != "" {
		t.Fatalf("g1 items = %v err=%v", first, err)
	}
	moved, err := s.MoveItems("r", g2.ID, "g1")
	if err != nil || len(moved) != 1 {
		t.Fatalf("moved = %v err=%v", moved, err)
	}
	merged, err := s.Items("r", "g1")
	if err != nil || len(merged) != 3 || merged[2].ID != "i3" || merged[2].Text != "three" {
		t.Fatalf("merged = %v err=%v", merged, err)
	}
	left, err := s.Items("r", g2.ID)
	if err != nil || len(left) != 0 {
		t.Fatalf("g2 items = %v err=%v", left, err)
	}
	if err := s.ReassignItems("r", "worker", "other"); err != nil {
		t.Fatal(err)
	}
	merged, err = s.Items("r", "g1")
	if err != nil || merged[0].Assignee != "worker" {
		t.Fatalf("done item assignee changed: %v err=%v", merged, err)
	}
	events, err := s.Events("r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var returns int
	for _, ev := range events {
		if ev.Event == "returned" {
			returns++
		}
	}
	if returns != 2 {
		t.Fatalf("returned events = %d: %+v", returns, events)
	}
}
