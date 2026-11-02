package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// host.list names sessions only in rooms the caller can read, by the same rule as session.list.
func TestHostListSessionsFollowRoomAccess(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	other, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	rooms := func(who contract.Principal) map[contract.RoomID]bool {
		t.Helper()
		list, problem := s.HostList(ctx, who, contract.HostListRequest{})
		if problem != nil || len(list.Hosts) != 2 {
			t.Fatalf("hosts: %+v %v", list, problem)
		}
		got := make(map[contract.RoomID]bool)
		for _, session := range list.Hosts[1].Sessions {
			got[session.Room] = true
		}
		return got
	}
	if got := rooms(owner); !got[room] || !got[other.Room] {
		t.Fatalf("owner: %+v", got)
	}
	if got := rooms(contract.Principal{Kind: contract.PrincipalSession, Session: worker}); !got[room] || got[other.Room] {
		t.Fatalf("session: %+v", got)
	}
	if got := rooms(contract.Principal{Kind: contract.PrincipalEngine}); len(got) != 0 {
		t.Fatalf("engine: %+v", got)
	}
}
