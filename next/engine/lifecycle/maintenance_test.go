package lifecycle

import (
	"context"
	"errors"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"testing"
	"time"
)

type maintenanceAdapter struct {
	BoxAdapter
	calls int
}

func (a *maintenanceAdapter) MaintainHost(_ context.Context, _ contract.HostName) error {
	a.calls++
	if a.calls == 1 {
		return errors.New("offline")
	}
	return nil
}

func TestDailyHostMaintenanceRetriesFailure(t *testing.T) {
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
	a := &maintenanceAdapter{}
	r := New(st, a, nil, nil, nil, func() time.Time { return now })
	pass := func() {
		t.Helper()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
	}
	pass()
	now = now.Add(time.Hour - time.Millisecond)
	pass()
	if a.calls != 1 {
		t.Fatalf("early retry: %d", a.calls)
	}
	now = now.Add(time.Millisecond)
	pass()
	if a.calls != 2 {
		t.Fatalf("retry: %d", a.calls)
	}
	now = now.Add(24*time.Hour - time.Millisecond)
	pass()
	if a.calls != 2 {
		t.Fatalf("early daily run: %d", a.calls)
	}
	now = now.Add(time.Millisecond)
	pass()
	if a.calls != 3 {
		t.Fatalf("daily run: %d", a.calls)
	}
}
