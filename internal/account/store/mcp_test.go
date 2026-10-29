package store

import "testing"

func TestMCPBearerRequiresRoom(t *testing.T) {
	s := &Store{Root: t.TempDir() + "/accounts"}
	if err := s.Update(func(st *State) error {
		_, err := st.SaveMCPBearer("room-a", "example", "fixture-token")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Entries) != 1 || state.Entries[0].RoomID != "room-a" || state.Entries[0].Provider != ProviderMCPBearer {
		t.Fatal("credential not scoped")
	}
	err = s.Update(func(st *State) error { st.Entries[0].RoomID = ""; return nil })
	if err == nil {
		t.Fatal("accepted empty room")
	}
}
