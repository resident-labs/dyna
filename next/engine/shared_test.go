package engine

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestHostListSharedSizes(t *testing.T) {
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
	if err := st.FinishHostMaintenance(ctx, "a", []contract.RoomShared{{Room: "old", Bytes: 1}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishHostMaintenance(ctx, "b", []contract.RoomShared{{Room: "other", Bytes: 3}}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishHostMaintenance(ctx, "a", []contract.RoomShared{{Room: "room", Bytes: 42}}, now); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil, func() time.Time { return now })
	got, problem := svc.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || len(got.Hosts) != 3 || len(got.Hosts[1].Shared) != 1 || got.Hosts[1].Shared[0] != (contract.RoomShared{Room: "room", Bytes: 42}) || len(got.Hosts[2].Shared) != 1 || got.Hosts[2].Shared[0].Bytes != 3 {
		t.Fatalf("host list: %+v, error: %v", got, problem)
	}
}
