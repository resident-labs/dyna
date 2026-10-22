package goals

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestJournalEntryRoundTrips(t *testing.T) {
	s := testStore(t)
	lines := []JournalLine{{Agent: "dyna-agent:worker", Status: "waiting", Summary: "Waits for CI on #12.", Goals: []string{"g1"}}}
	added, err := s.AddJournal("r", JournalEntry{Entries: lines})
	if err != nil || added.ID != "j0" || added.NeedsAction || !added.At.Equal(s.now()) {
		t.Fatalf("%+v %v", added, err)
	}
	blocked, err := s.AddJournal("r", JournalEntry{Entries: []JournalLine{{Agent: "dyna-agent:worker", Status: "blocked", Summary: "Needs a key."}}})
	if err != nil || !blocked.NeedsAction || blocked.ID == added.ID {
		t.Fatalf("%+v %v", blocked, err)
	}
	got, older, err := (&Store{Root: s.Root}).Journal("r", "", 0)
	if err != nil || older || len(got) != 2 || got[0].ID != blocked.ID || !reflect.DeepEqual(got[1].Entries, lines) {
		t.Fatalf("%+v %v %v", got, older, err)
	}
	// The journal is a file of its own.
	if _, err := os.Stat(filepath.Join(s.Root, "journal", "r.jsonl")); err != nil {
		t.Fatal(err)
	}
}

// Each entry is larger than a few hundred bytes, so 300 of them span several
// backward read chunks.
func TestJournalPagesBackwardAcrossReadChunks(t *testing.T) {
	s := testStore(t)
	var ids []string
	for i := range 300 {
		e, err := s.AddJournal("r", JournalEntry{Entries: []JournalLine{{Agent: "a", Status: "on_track", Summary: fmt.Sprintf("%03d %s", i, strings.Repeat("x", 300))}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	all, older, err := s.Journal("r", "", 0)
	if err != nil || older || len(all) != 300 || all[0].ID != ids[299] || all[299].ID != ids[0] {
		t.Fatalf("len=%d older=%v err=%v", len(all), older, err)
	}
	var seen []string
	cursor := ""
	for {
		page, older, err := s.Journal("r", cursor, 70)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page {
			seen = append(seen, e.ID)
		}
		if !older {
			break
		}
		cursor = page[len(page)-1].ID
	}
	if len(seen) != 300 || seen[0] != ids[299] || seen[299] != ids[0] {
		t.Fatalf("paging returned %d entries", len(seen))
	}
	if page, older, err := s.Journal("r", "", 300); err != nil || older || len(page) != 300 {
		t.Fatalf("a full last page reports older entries: len=%d older=%v err=%v", len(page), older, err)
	}
	for _, bad := range []string{"j1", "x0", "j99999999"} {
		if _, _, err := s.Journal("r", bad, 1); err == nil {
			t.Fatalf("cursor %q was accepted", bad)
		}
	}
}

func TestJournalSkipsAPartialLineFromACrashedWrite(t *testing.T) {
	s := testStore(t)
	first, err := s.AddJournal("r", JournalEntry{Entries: []JournalLine{{Agent: "a", Status: "on_track", Summary: "First."}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "journal", "r.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(`{"id":"j9","entries":[{"age`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got, _, err := s.Journal("r", "", 0); err != nil || len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("%+v %v", got, err)
	}
	next, err := s.AddJournal("r", JournalEntry{Entries: []JournalLine{{Agent: "a", Status: "waiting", Summary: "Next."}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Journal("r", "", 0)
	if err != nil || len(got) != 2 || got[0].ID != next.ID || got[1].ID != first.ID {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestRoomWithoutJournalHasNoEntries(t *testing.T) {
	s := testStore(t)
	got, older, err := s.Journal("r", "", 10)
	if err != nil || older || len(got) != 0 || got == nil {
		t.Fatalf("%+v %v", got, err)
	}
	if _, _, err := s.Journal("../r", "", 10); err == nil {
		t.Fatal("a path was accepted as a room ID")
	}
}

func TestJournalAppendsEachRoundOnce(t *testing.T) {
	s := testStore(t)
	lines := []JournalLine{{Agent: "a", Status: "blocked", Summary: "Needs a key."}}
	at := s.now()
	a, err := s.AddJournal("r", JournalEntry{Round: "A", At: at, Entries: lines})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddJournal("r", JournalEntry{Round: "B", At: at.Add(time.Minute), Entries: lines})
	if err != nil {
		t.Fatal(err)
	}
	// Retries of both rounds, in the original order, after B was appended.
	for _, retry := range []JournalEntry{{Round: "A", At: at, Entries: lines}, {Round: "B", At: at.Add(time.Minute), Entries: lines}} {
		got, err := s.AddJournal("r", retry)
		if err != nil || got.ID != map[string]string{"A": a.ID, "B": b.ID}[retry.Round] || !got.NeedsAction {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if got, _, err := s.Journal("r", "", 0); err != nil || len(got) != 2 {
		t.Fatalf("a retried round appended twice: %+v %v", got, err)
	}
}
