package store

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestNewRoomHasEveryRootApproval(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(t.Context(), "r", "room", time.Now()); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.Read().QueryRowContext(t.Context(), "SELECT root_approves FROM rooms WHERE id='r'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got []contract.RootApproval
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, contract.AllRootApprovals) {
		t.Fatalf("got %v want %v", got, contract.AllRootApprovals)
	}
}
