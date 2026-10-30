package issuewatch

import "testing"

// Advance writes only for the token the watch was saved with. A removed watch
// and a re-saved watch both refuse the older poll's cursor.
func TestAdvanceRefusesStaleTokens(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	base := Watch{Room: "room", Host: "github.com", Repo: "owner/repo", Number: 3}
	if err := s.Save(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	first := onlyWatch(t, s)
	if err := s.Save(t.Context(), base); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Advance(t.Context(), first); err != nil || ok {
		t.Fatalf("advance with a replaced token: ok=%v err=%v, want false and nil", ok, err)
	}
	second := onlyWatch(t, s)
	if ok, err := s.Remove(t.Context(), "room", "github.com", "owner/repo", 3); err != nil || !ok {
		t.Fatalf("remove: ok=%v err=%v, want true and nil", ok, err)
	}
	if ok, err := s.Advance(t.Context(), second); err != nil || ok {
		t.Fatalf("advance after remove: ok=%v err=%v, want false and nil", ok, err)
	}
}

// Room lists only that room's watches, by repository and number, so the
// Issues tab's Tracked view never shows another room's issues.
func TestRoomListsOnlyItsOwnWatches(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	for _, w := range []Watch{
		{Room: "room", Host: "github.com", Repo: "up/repo", Number: 9, Title: "Nine", State: "open"},
		{Room: "other", Host: "github.com", Repo: "up/repo", Number: 4},
		{Room: "room", Host: "github.com", Repo: "up/repo", Number: 2, State: "closed"},
	} {
		if err := s.Save(t.Context(), w); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Room(t.Context(), "room")
	if err != nil || len(got) != 2 || got[0].Number != 2 || got[1].Number != 9 || got[1].Title != "Nine" || got[0].State != "closed" {
		t.Fatalf("Room = %+v err=%v, want #2 closed then #9 Nine", got, err)
	}
}

func onlyWatch(t *testing.T, s Store) Watch {
	t.Helper()
	watches, err := s.List(t.Context())
	if err != nil || len(watches) != 1 {
		t.Fatalf("list: got %d watches err=%v, want one", len(watches), err)
	}
	return watches[0]
}
