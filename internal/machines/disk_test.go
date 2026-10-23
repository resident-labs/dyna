package machines

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// lowDiskProbe is what a CI machine prints when it is below its disk floor.
const lowDiskProbe = `os=Linux
arch=x86_64
cpus=16
disktotal=1932735283200
diskfree=3221225472`

// roomyProbe is the same machine once its space is back.
const roomyProbe = `os=Linux
arch=x86_64
cpus=16
disktotal=1932735283200
diskfree=214748364800`

// diskAlerts records the low-disk alerts a prober posted.
type diskAlerts struct{ got []string }

func (a *diskAlerts) MachineLowDisk(_ context.Context, name string, free, floor uint64) error {
	a.got = append(a.got, fmt.Sprintf("%s %d %d", name, free, floor))
	return nil
}

// prints answers a probe with this script output.
func prints(out string) Command {
	return func(_ context.Context, _ string, _ io.Reader, w, _ io.Writer, _ string, _ ...string) error {
		_, err := io.WriteString(w, out)
		return err
	}
}

// A CI machine below the disk floor is degraded with the low-disk reason and
// holds its CI work. The hold survives a probe that cannot reach the machine,
// and the alert posts once per low-disk period, not once per probe.
func TestLowDiskDegradesAndAlertsOnce(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	hermes, err := s.Save(ctx, Machine{Name: "hermeslab", Roles: []Role{RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	alerts := &diskAlerts{}
	prober := Prober{
		Store:          s,
		Command:        prints(lowDiskProbe),
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

	// A probe that cannot reach the machine keeps the hold: only a probe that
	// reached the machine can show that space came back.
	unreachable := func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		return errors.New("ssh: connect to host pip@hermeslab port 22: Connection refused")
	}
	prober.Command = unreachable
	offline, err := prober.ProbeAndSave(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	if offline.Status() != HealthOffline || !strings.Contains(offline.ProbeError, "Connection refused") {
		t.Fatalf("machine after an unreachable probe = %s (%q)", offline.Status(), offline.ProbeError)
	}
	if !offline.HoldsCI() {
		t.Fatal("a low-disk hold ended because a probe could not reach the machine")
	}
	if len(alerts.got) != 1 {
		t.Fatalf("an unreachable probe posted an alert: %v", alerts.got)
	}

	// Space returns: the machine is online and takes CI work again.
	prober.Command = prints(roomyProbe)
	recovered, err := prober.ProbeAndSave(ctx, offline)
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
	prober.Command = prints(lowDiskProbe)
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
	prober.Command = prints(lowDiskProbe)
	if _, err := prober.ProbeAndSave(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if len(alerts.got) != 2 {
		t.Fatalf("a machine with no ci role posted an alert: %v", alerts.got)
	}
}

// A probe that is already in flight when the owner turns the machine off stores
// disabled, with no reason and no readings: the owner's switch wins, and
// enabling the machine again starts it unknown.
func TestDisableDuringProbeWins(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	hermes, err := s.Save(ctx, Machine{Name: "hermeslab", Roles: []Role{RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// The probe holds the row as it was before the owner switched it off.
	if err := s.SetEnabled(ctx, hermes.Name, false); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveProbe(ctx, hermes.ID, ProbeResult{Health: HealthDegraded, Reason: ReasonLowDisk, Capabilities: Capabilities{CPUs: 16}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status() != HealthDisabled || saved.HealthReason != ReasonNone || saved.HoldsCI() {
		t.Fatalf("machine after a probe that outlived the switch = %s (%q)", saved.Status(), saved.HealthReason)
	}
	if saved.Capabilities.CPUs != 0 {
		t.Fatalf("a probe stored readings on a machine that was switched off: %#v", saved.Capabilities)
	}
	if err := s.SetEnabled(ctx, hermes.Name, true); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.Get(ctx, hermes.Name)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status() != HealthUnknown || enabled.HealthReason != ReasonNone {
		t.Fatalf("machine after it was enabled again = %s (%q), want unknown", enabled.Status(), enabled.HealthReason)
	}
}
