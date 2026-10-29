package machines

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// TestKeepFreeBytesMatchesTheBlockBoundary checks that the recovery boundary
// and the admission rule are one boundary: free space at or over
// KeepFreeBytes admits work, and less than it blocks, with and without a
// percent limit.
func TestKeepFreeBytesMatchesTheBlockBoundary(t *testing.T) {
	total := uint64(100) << 30
	if keep := KeepFreeBytes(0, total); keep != 10<<30 {
		t.Fatalf("KeepFreeBytes without a limit = %d bytes, want the disk's tenth", keep)
	}
	for _, tc := range []struct {
		maxPercent int
		free       uint64
		blocks     bool
	}{
		{0, 10 << 30, false},
		{0, 10<<30 - 1, true},
		{90, 10<<30 + 1, false},
		{90, 10 << 30, true},
	} {
		if blocked := diskBlock(tc.maxPercent, Disk{Total: total, Free: tc.free}) != ""; blocked != tc.blocks {
			t.Fatalf("diskBlock(max %d%%, %d GiB free) blocked = %v, want %v", tc.maxPercent, tc.free>>30, blocked, tc.blocks)
		}
	}
	if keep := KeepFreeBytes(90, 0); keep != 0 {
		t.Fatalf("KeepFreeBytes for a never-probed disk = %d, want 0", keep)
	}
}

// lowDiskProbe is what a machine prints when its disk is too full for the
// queue's floor: 3 GiB free of 1800 GiB, under the 5% share the queue keeps.
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

func (a *diskAlerts) MachineLowDisk(_ context.Context, name, block string, free uint64) error {
	a.got = append(a.got, fmt.Sprintf("%s|%s|%d", name, block, free))
	return nil
}

// prints answers a probe with this script output.
func prints(out string) Command {
	return func(_ context.Context, _ string, _ io.Reader, w, _ io.Writer, _ string, _ ...string) error {
		_, err := io.WriteString(w, out)
		return err
	}
}

// A node whose disk starts to block alerts once, with the words DiskBlock
// returns, and not again while it still blocks: a probe that cannot reach the
// node keeps the block, and only space coming back ends the period. The node
// stays online while it blocks; the queue holds the work, not the health.
func TestLowDiskAlertsWhenTheQueueStartsToBlock(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	hermes, err := s.Save(ctx, Machine{Name: "buildhost", SSH: "pip@hermeslab", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	alerts := &diskAlerts{}
	prober := Prober{
		Store:   s,
		Command: prints(lowDiskProbe),
		Alerter: alerts,
		Now:     func() time.Time { return time.Unix(1, 0) },
	}

	stored, err := prober.ProbeAndSave(ctx, hermes)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status() != HealthOnline {
		t.Fatalf("health = %s, want online: a blocked disk is no health of its own", stored.Status())
	}
	if block := stored.DiskBlock(); block != "disk has 3 GiB free, below the 90 GiB floor" {
		t.Fatalf("DiskBlock() = %q", block)
	}
	// The machine answered, so the owner still sees what it has.
	if stored.Capabilities.Disk.Free != 3221225472 || stored.LastSeen.IsZero() {
		t.Fatalf("blocked probe = %#v", stored)
	}
	if len(alerts.got) != 1 || alerts.got[0] != "buildhost|disk has 3 GiB free, below the 90 GiB floor|3221225472" {
		t.Fatalf("alerts = %v", alerts.got)
	}

	// A second probe of the same blocked disk is no new period.
	if _, err := prober.ProbeAndSave(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(alerts.got) != 1 {
		t.Fatalf("a repeated blocked probe alerted again: %v", alerts.got)
	}

	// A probe that cannot reach the machine keeps the block, because it keeps
	// the capabilities the last good probe read. It posts no alert.
	unreachable := func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		return fmt.Errorf("ssh: connect to host pip@hermeslab port 22: Connection refused")
	}
	prober.Command = unreachable
	offline, err := prober.ProbeAndSave(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	if offline.Status() != HealthOffline || !strings.Contains(offline.ProbeError, "Connection refused") {
		t.Fatalf("machine after an unreachable probe = %s (%q)", offline.Status(), offline.ProbeError)
	}
	if offline.DiskBlock() == "" {
		t.Fatal("an unreachable probe ended the block")
	}
	if len(alerts.got) != 1 {
		t.Fatalf("an unreachable probe posted an alert: %v", alerts.got)
	}

	// Space returns: the node admits work again and posts no alert.
	prober.Command = prints(roomyProbe)
	recovered, err := prober.ProbeAndSave(ctx, offline)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status() != HealthOnline || recovered.DiskBlock() != "" {
		t.Fatalf("recovered machine = %s (%q)", recovered.Status(), recovered.DiskBlock())
	}
	if len(alerts.got) != 1 {
		t.Fatalf("recovering posted an alert: %v", alerts.got)
	}

	// The next blocked period is its own alert.
	prober.Command = prints(lowDiskProbe)
	if _, err := prober.ProbeAndSave(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	if len(alerts.got) != 2 {
		t.Fatalf("a second blocked period posted %d alerts, want 2", len(alerts.got))
	}
}

// The disk rule is the queue's, so it holds every node's work whatever its
// roles: a machine without the ci role alerts too.
func TestLowDiskAlertsWithoutTheCIRole(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	agent, err := s.Save(ctx, Machine{Name: "agent-box", SSH: "aria@agent-box", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	alerts := &diskAlerts{}
	prober := Prober{Store: s, Command: prints(lowDiskProbe), Alerter: alerts, Now: func() time.Time { return time.Unix(1, 0) }}
	stored, err := prober.ProbeAndSave(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DiskBlock() == "" || len(alerts.got) != 1 {
		t.Fatalf("block = %q, alerts = %v", stored.DiskBlock(), alerts.got)
	}
}

// A probe that is already in flight when the owner turns the machine off stores
// disabled, with no readings: the owner's switch wins, and enabling the machine
// again starts it unknown.
func TestDisableDuringProbeWins(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	hermes, err := s.Save(ctx, Machine{Name: "buildhost", SSH: "pip@hermeslab", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// The probe holds the row as it was before the owner switched it off.
	if err := s.SetEnabled(ctx, hermes.Name, false); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveProbe(ctx, hermes.ID, ProbeResult{Health: HealthOnline, Capabilities: Capabilities{CPUs: 16}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status() != HealthDisabled {
		t.Fatalf("machine after a probe that outlived the switch = %s", saved.Status())
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
	if enabled.Status() != HealthUnknown {
		t.Fatalf("machine after it was enabled again = %s, want unknown", enabled.Status())
	}
}

// A node keeps admitting work while its free space is at least the larger of
// 20 GiB and 5% of the disk, however full the disk is in percent: a 1.8 TiB
// disk with 96 GiB free is not full. The floor never asks for more than a
// tenth of the disk, so a small disk can still run work. A set percent limit
// overrides the floor.
func TestDiskBlocksWorkByFreeSpace(t *testing.T) {
	const gib = uint64(1) << 30
	cases := []struct {
		name        string
		total, free uint64
		maxPercent  int
		blocked     bool
	}{
		{"big disk 95% used, 96 GiB free", 1843 * gib, 96 * gib, 0, false},
		{"big disk below 5% free", 1843 * gib, 80 * gib, 0, true},
		{"mid disk above the 20 GiB floor", 300 * gib, 25 * gib, 0, false},
		{"mid disk below the 20 GiB floor", 300 * gib, 15 * gib, 0, true},
		{"small disk keeps a tenth free", 100 * gib, 15 * gib, 0, false},
		{"small disk below a tenth free", 100 * gib, 8 * gib, 0, true},
		{"empty disk below 20 GiB", 16 * gib, 16 * gib, 0, false},
		{"full disk below 20 GiB", 16 * gib, gib, 0, true},
		{"percent override blocks", 1843 * gib, 96 * gib, 90, true},
		{"percent override admits", 100 * gib, 15 * gib, 90, false},
		{"never probed", 0, 0, 0, false},
	}
	for _, tc := range cases {
		m := Machine{MaxDiskPercent: tc.maxPercent, Capabilities: Capabilities{Disk: Disk{Total: tc.total, Free: tc.free}}}
		why := m.DiskBlock()
		if (why != "") != tc.blocked {
			t.Fatalf("%s: DiskBlock() = %q, want blocked=%v", tc.name, why, tc.blocked)
		}
	}
}

// DiskKeeps words the rule the node's disk follows while it admits work, so
// the page and the command line show one line with one rule.
func TestDiskKeepsWordsTheRule(t *testing.T) {
	cases := []struct {
		name       string
		total      uint64
		maxPercent int
		want       string
	}{
		{"never probed", 0, 0, ""},
		{"mid disk keeps 20 GiB and 5%", 300 << 30, 0, "20 GiB and 5% of the disk"},
		{"big disk keeps its share", 1800 << 30, 0, "5% of the disk"},
		{"small disk keeps a tenth", 100 << 30, 0, "a tenth of the disk"},
		{"percent limit", 300 << 30, 90, "used under 90%"},
	}
	for _, tc := range cases {
		m := Machine{MaxDiskPercent: tc.maxPercent, Capabilities: Capabilities{Disk: Disk{Total: tc.total}}}
		if got := m.DiskKeeps(); got != tc.want {
			t.Fatalf("%s: DiskKeeps() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
