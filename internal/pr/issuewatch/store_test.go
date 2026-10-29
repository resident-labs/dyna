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

func onlyWatch(t *testing.T, s Store) Watch {
	t.Helper()
	watches, err := s.List(t.Context())
	if err != nil || len(watches) != 1 {
		t.Fatalf("list: got %d watches err=%v, want one", len(watches), err)
	}
	return watches[0]
}
