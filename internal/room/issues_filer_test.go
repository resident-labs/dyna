package room

import "testing"

// The filer address resolves to one typed kind, which filing and noting read.
func TestFilerActorKinds(t *testing.T) {
	s, rec, _ := pinFixture(t)
	if _, err := s.Create(CreateInput{Name: "beta", CreatedBy: "tester"}); err != nil {
		t.Fatal(err)
	}
	for by, want := range map[string]FilerKind{
		"alpha":        FilerRoomOwner,
		"alpha:agent":  FilerRoomSession,
		"beta":         FilerOtherRoom,
		"beta:captain": FilerOtherRoom,
		"ghost:agent":  FilerUnknown,
		":agent":       FilerUnknown,
	} {
		if got := s.filerActor(rec, by); got != want {
			t.Errorf("filerActor(%q) = %d, want %d", by, got, want)
		}
	}
}
