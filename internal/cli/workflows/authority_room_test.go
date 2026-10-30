package workflows

import (
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/store/storetest"
)

func TestRunRoomSelectionKeepsOwnerAuthority(t *testing.T) {
	inRepo(t)
	storetest.ClearStamp(t)
	if actor := authority.Current(); actor.Role != authority.Owner {
		t.Fatalf("caller = %+v", actor)
	}
	rec, err := applyRunRoom("")
	if err != nil {
		t.Fatal(err)
	}
	actor := authority.Current()
	if actor.Role != authority.Owner || actor.Room != rec.ID {
		t.Fatalf("run actor = %+v", actor)
	}
	if err := authority.Check("workflow run", actor, nil); err != nil {
		t.Fatal(err)
	}
}
