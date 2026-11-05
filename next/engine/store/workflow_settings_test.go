package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// A deleted room takes its workflow overrides along and leaves the global row.
func TestDeletingARoomRemovesItsWorkflowSettings(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	room := contract.RoomID("room_gone")
	if err := s.CreateRoom(ctx, room, "gone", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	off := false
	for _, scope := range []contract.RoomID{GlobalWorkflowScope, room} {
		if err := s.ChangeWorkflowSettings(ctx, scope, func(contract.WorkflowSettings) contract.WorkflowSettings {
			return contract.WorkflowSettings{Post: &off}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.writer.ExecContext(ctx, "UPDATE rooms SET state='deleting' WHERE id=?", room); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoom(ctx, room); err != nil {
		t.Fatal(err)
	}
	all, err := s.WorkflowSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all[room]; ok || all[GlobalWorkflowScope].Post == nil {
		t.Fatalf("settings after the delete: %+v", all)
	}
}
