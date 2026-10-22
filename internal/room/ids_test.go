package room

import "testing"

func TestMessageIDSelectionVisitsOnlyAppendedLines(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "selected-ids", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	post := func(text string) {
		t.Helper()
		if _, err := s.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	post("one")
	post("two")
	post("three")
	selection := &MessageSelection{}
	visited := 0
	match := func(Message) bool { visited++; return true }
	ids, err := s.MessageIDs(rec.ID, selection, match)
	if err != nil || len(ids) != 3 || visited != 3 {
		t.Fatalf("first selection: %d IDs, %d visits, %v", len(ids), visited, err)
	}
	visited = 0
	ids, err = s.MessageIDs(rec.ID, selection, match)
	if err != nil || len(ids) != 3 || visited != 0 {
		t.Fatalf("warm selection: %d IDs, %d visits; want 3 IDs and 0 visits: %v", len(ids), visited, err)
	}
	post("four")
	visited = 0
	ids, err = s.MessageIDs(rec.ID, selection, match)
	if err != nil || len(ids) != 4 || visited != 1 {
		t.Fatalf("append selection: %d IDs, %d visits; want 4 IDs and 1 visit: %v", len(ids), visited, err)
	}
}
