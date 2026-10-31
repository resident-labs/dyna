package engine

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestHostListReportsFullDisk(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err := st.AddHost(ctx, box.Host{Name: "full", State: box.HostReady, DiskBytes: 30 * box.GiB, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	free := int64(0)
	if err := st.RecordHostHeartbeat(ctx, "full", true, &free, now); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, nil, time.Now)
	got, problem := svc.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
	if problem != nil || len(got.Hosts) != 2 || !got.Hosts[1].DiskLow {
		t.Fatalf("host list: %+v, error: %v", got, problem)
	}
}
