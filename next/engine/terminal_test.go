package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestHostListOwner(t *testing.T) {
	s, _, _, ctx := setup(t)
	list, problem := s.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || len(list.Hosts) != 2 || list.Hosts[0].Name != contract.ControlPlane || list.Hosts[1].Name != "host" {
		t.Fatalf("hosts: %+v %v", list, problem)
	}
	if list.Hosts[1].EngineHost {
		t.Fatalf("a host is not the engine's machine until the engine compares them: %+v", list.Hosts[1])
	}
	if err := s.store.SetEngineHost(ctx, "host", true); err != nil {
		t.Fatal(err)
	}
	list, problem = s.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || !list.Hosts[1].EngineHost || list.Hosts[0].EngineHost {
		t.Fatalf("engine host: %+v %v", list, problem)
	}
}

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

func TestHostListBackupFailureCodes(t *testing.T) {
	s, _, _, ctx := setup(t)
	for _, test := range []struct {
		failure string
		want    contract.BackupCopyFailure
	}{
		{"private diagnostic", contract.BackupCopyFailed},
		{store.BackupCopyNoSpace, contract.BackupCopyNoSpace},
	} {
		if err := s.store.RecordBackupCopy(ctx, time.Now(), test.failure); err != nil {
			t.Fatal(err)
		}
		list, problem := s.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
		if problem != nil || list.Hosts[0].BackupCopyFailure != test.want {
			t.Fatalf("failure: %+v %v", list, problem)
		}
	}
}

func TestTerminalKeepsBoxAwake(t *testing.T) {
	s, host, _, sid, ctx := quietSession(t, 0)
	_, release, problem := s.Terminal(ctx, sid, "")
	if problem != nil {
		t.Fatal(problem)
	}
	passLifecycle(t, s, ctx)
	if host.stops != 0 {
		t.Fatal("terminal box stopped")
	}
	release()
	passLifecycle(t, s, ctx)
	if host.stops != 1 {
		t.Fatal("box did not stop after terminal closed")
	}
	_, _, problem = s.Terminal(ctx, sid, "")
	if problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("stopped box: %v", problem)
	}
}
