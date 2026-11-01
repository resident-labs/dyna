package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestHostListGivesTheSharedSizesOnce(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(200000, 0)
	for _, name := range []contract.HostName{"a", "b"} {
		if err := st.AddHost(ctx, box.Host{Name: name, State: box.HostReady, CheckedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetRoomShared(ctx, []contract.RoomShared{{Room: "old", Bytes: 1}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRoomShared(ctx, []contract.RoomShared{{Room: "room", Bytes: 42}, {Room: "other", Bytes: 3}}, now); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil, func() time.Time { return now })
	got, problem := svc.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || len(got.Hosts) != 3 || got.Hosts[0].Name != contract.ControlPlane {
		t.Fatalf("host list: %+v, error: %v", got, problem)
	}
	if want := []contract.RoomShared{{Room: "other", Bytes: 3}, {Room: "room", Bytes: 42}}; !slices.Equal(got.Hosts[0].Shared, want) || len(got.Hosts[1].Shared) != 0 || len(got.Hosts[2].Shared) != 0 {
		t.Fatalf("host list: %+v, error: %v", got, problem)
	}
}
