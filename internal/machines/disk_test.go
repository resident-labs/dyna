package machines

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"
)

// lowDiskProbe is what a CI machine prints when it is below its disk floor.
const lowDiskProbe = `os=Linux
arch=x86_64
cpus=16
disktotal=1932735283200
diskfree=3221225472`

// diskAlerts records the low-disk alerts a prober posted.
type diskAlerts struct{ got []string }

func (a *diskAlerts) MachineLowDisk(_ context.Context, name string, free, floor uint64) error {
	a.got = append(a.got, fmt.Sprintf("%s %d %d", name, free, floor))
	return nil
}

// A CI machine below the disk floor is degraded with the low-disk reason, and
// holds its CI work. The alert posts once per low-disk period, not once per
// probe.
func TestLowDiskDegradesAndAlertsOnce(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	hermes, err := s.Save(ctx, Machine{Name: "hermeslab", Roles: []Role{RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	probe := func(out string) Command {
		return func(_ context.Context, _ string, _ io.Reader, w, _ io.Writer, _ string, _ ...string) error {
			_, err := io.WriteString(w, out)
			return err
		}
	}
	alerts := &diskAlerts{}
	prober := Prober{
		Store:          s,
		Command:        probe(lowDiskProbe),
		DiskFloorBytes: 20 << 30,
		Alerter:        alerts,
		Now:            func() time.Time { return time.Unix(1, 0) },
	}

	stored, err := prober.ProbeAndSave(ctx, hermes)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status() != HealthDegraded || stored.HealthReason != ReasonLowDisk {
		t.Fatalf("health = %s (%q), want degraded low-disk", stored.Status(), stored.HealthReason)
	}
	if !stored.HoldsCI() {
		t.Fatal("a machine below the disk floor does not hold CI")
	}
	// The machine answered, so the owner still sees what it has.
	if stored.Capabilities.Disk.Free != 3221225472 || stored.LastSeen.IsZero() {
		t.Fatalf("degraded probe = %#v", stored)
	}
	if len(alerts.got) != 1 || alerts.got[0] != "hermeslab 3221225472 21474836480" {
		t.Fatalf("alerts = %v", alerts.got)
	}

	// A second probe of the same low disk is no new transition.
	if _, err := prober.ProbeAndSave(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(alerts.got) != 1 {
		t.Fatalf("a repeated low-disk probe alerted again: %v", alerts.got)
	}

	// Space returns: the machine is online and takes CI work again.
	prober.Command = probe(`os=Linux
arch=x86_64
cpus=16
disktotal=1932735283200
diskfree=214748364800`)
	recovered, err := prober.ProbeAndSave(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status() != HealthOnline || recovered.HealthReason != ReasonNone || recovered.HoldsCI() {
		t.Fatalf("recovered machine = %s (%q)", recovered.Status(), recovered.HealthReason)
	}
	if len(alerts.got) != 1 {
		t.Fatalf("recovering posted an alert: %v", alerts.got)
	}

	// The next low-disk period is its own alert.
	prober.Command = probe(lowDiskProbe)
	if _, err := prober.ProbeAndSave(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	if len(alerts.got) != 2 {
		t.Fatalf("a second low-disk period posted %d alerts, want 2", len(alerts.got))
	}

	// A machine without the ci role keeps no floor: it runs no CI to hold.
	agent, err := s.Save(ctx, Machine{Name: "agent-box", Roles: []Role{RoleAgents}, SSH: "aria@agent-box", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	prober.Command = probe(lowDiskProbe)
	if _, err := prober.ProbeAndSave(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if len(alerts.got) != 2 {
		t.Fatalf("a machine with no ci role posted an alert: %v", alerts.got)
	}
}
