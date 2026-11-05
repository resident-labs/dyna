package lifecycle

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
)

func TestReviewUpdateRefreshesLatestSourceEstimate(t *testing.T) {
	rig := newServiceRig(t)
	rig.sql("UPDATE boxes SET state='deleted'")
	rig.sql("UPDATE hosts SET disk_floor=0,disk_bytes=2147483648")
	rig.declare(contract.ServiceSpec{Name: "web", Image: "app", Disk: "0GiB"})
	row := rig.settle()
	if row.State != contract.ServiceRunning {
		t.Fatalf("initial deploy: %+v", row.Service)
	}
	// Another completed build of the same room source has since measured 1 GiB.
	if err := rig.st.RecordServiceImage(rig.ctx, row.Room, row.Parts, 1<<30, rig.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	rig.sql("UPDATE hosts SET disk_bytes=536870912")
	before := count(rig.host.Calls, fakehost.ImageSize)
	if _, err := rig.r.UpdateService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	current := rig.settle()
	after := count(rig.host.Calls, fakehost.ImageSize)
	if after != before || current.Reason != contract.ServiceNoDisk {
		t.Fatalf("update entered image work despite latest 1 GiB estimate and 512 MiB free: before=%d after=%d row=%+v imageBytes=%d estimate=%d", before, after, current.Service, current.ImageBytes, current.ImageEstimate)
	}
}
