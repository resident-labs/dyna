package lifecycle

import (
	"context"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"testing"
	"time"
)

type maintenanceAdapter struct {
	BoxAdapter
	calls chan contract.HostName
}

func (a maintenanceAdapter) MaintainHost(_ context.Context, host contract.HostName) error {
	a.calls <- host
	return nil
}

func TestDailyHostMaintenance(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(200000, 0)
	if err := st.AddHost(ctx, box.Host{Name: "h", State: box.HostReady, DiskBytes: 40 * box.GiB, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimHostMaintenance(ctx, "h", now.Add(-24*time.Hour)); !ok || err != nil {
		t.Fatalf("initial claim: %v %v", ok, err)
	}
	a := maintenanceAdapter{calls: make(chan contract.HostName, 2)}
	r := New(st, a, nil, nil, nil, func() time.Time { return now })
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if err := r.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if len(a.calls) != 1 || <-a.calls != "h" {
		t.Fatal("maintenance did not run exactly once")
	}
}
