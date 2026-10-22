package notices

import "testing"

func TestPostDedupesByKeyAndDismissRemoves(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	first, err := s.Post(Notice{Key: "provider:argon", Kind: "provider", Severity: "warn", Text: "argon: quota reached, resets in 152h"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(Notice{Key: "disk", Kind: "disk", Severity: "error", Text: "Disk space is low."}); err != nil {
		t.Fatal(err)
	}
	again, err := s.Post(Notice{Key: "provider:argon", Kind: "provider", Severity: "warn", Text: "argon: quota reached, resets in 151h"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("same key got a new ID: %s != %s", again.ID, first.ID)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].Text != "argon: quota reached, resets in 151h" || list[0].At.IsZero() {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.Dismiss(first.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].Key != "disk" {
		t.Fatalf("after dismiss = %+v", list)
	}
	back, _ := s.Post(Notice{Key: "provider:argon", Kind: "provider", Severity: "warn", Text: "again"})
	if back.ID == first.ID {
		t.Fatal("a dismissed key must come back as a new notice")
	}
}

func TestListEmpty(t *testing.T) {
	list, err := Store{Dir: t.TempDir()}.List()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("list = %#v, %v", list, err)
	}
}
