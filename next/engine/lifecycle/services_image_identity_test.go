package lifecycle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestUnknownImageMustNotInheritPreviousImagesMeasuredSize(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	rig.declare(spec)
	old := rig.settle()
	spec.Image = digestImage2 // never pulled or measured on any host
	updated := rig.declare(spec)
	estimate, err := rig.st.ImageEstimate(rig.ctx, updated.Room, updated.Parts)
	if err != nil {
		t.Fatal(err)
	}
	if estimate != contract.ServiceImageEstimate {
		t.Fatalf("unknown image B inherited old image A's size: returned=%d fallback=%d oldImageBytes=%d pendingEstimate=%d oldRef=%s desired=%s", estimate, contract.ServiceImageEstimate, old.ImageBytes, updated.ImageEstimate, updated.ImageRef, updated.Spec.Image)
	}
}

func TestServiceImageMeasurementKeepsItsIdentityAfterFailedHealth(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	spec.Health = []string{"/bin/true"}
	rig.declare(spec)
	old := rig.settle()
	rig.host.Service.Health = func(_ contract.ServiceID, file string) error {
		if strings.Contains(file, "bbbbbbbb") {
			return errors.New("unhealthy")
		}
		return nil
	}
	spec.Image = digestImage2
	rig.declare(spec)
	row := rig.settle()
	if row.Reason != contract.ServiceRolledBack {
		t.Fatalf("not rolled back: %+v", row.Service)
	}
	for _, parts := range []contract.ServiceParts{old.Parts, row.Parts} {
		got, err := rig.st.ImageEstimate(rig.ctx, row.Room, parts)
		if err != nil || got != 100<<20 {
			t.Fatalf("measurement of %s: %d, %v", parts.Spec.Image, got, err)
		}
	}
}

func TestServiceImageMeasurementsScopeRoomSourcesAndShareRegistryDigests(t *testing.T) {
	rig := newServiceRig(t)
	parse := func(image string) contract.ServiceParts {
		parts, err := (contract.ServiceSpec{Name: "web", Image: image}).Parse()
		if err != nil {
			t.Fatal(err)
		}
		return parts
	}
	app, registry := parse("app"), parse(digestImage)
	for _, sample := range []struct {
		room  contract.RoomID
		parts contract.ServiceParts
		size  int64
	}{
		{"r", app, 100 << 20}, {"other", app, 2 << 30}, {"r", registry, 3 << 30},
	} {
		if err := rig.st.RecordServiceImage(rig.ctx, sample.room, sample.parts, sample.size, rig.now); err != nil {
			t.Fatal(err)
		}
	}
	// An older report arriving later does not replace the newest measurement.
	if err := rig.st.RecordServiceImage(rig.ctx, "r", app, 1, rig.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		room  contract.RoomID
		parts contract.ServiceParts
		want  int64
	}{
		{"r", app, 100 << 20}, {"other", app, 2 << 30}, {"new-room", app, contract.ServiceImageEstimate}, {"other", registry, 3 << 30},
	} {
		got, err := rig.st.ImageEstimate(rig.ctx, tc.room, tc.parts)
		if err != nil || got != tc.want {
			t.Errorf("room %s, image %s: %d, %v; want %d", tc.room, tc.parts.Spec.Image, got, err, tc.want)
		}
	}
}
