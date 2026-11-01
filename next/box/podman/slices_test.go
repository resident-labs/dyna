package podman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestHeartbeatCountsAnonShmemAndSwap(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"memory.stat": "anon 100\nshmem 30\nfile 900\n", "memory.swap.current": "20\n", "cpu.stat": "usage_usec 42\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `df() { return 1; }; systemctl() { echo /; }; ` + strings.Replace(heartbeatScript, `/sys/fs/cgroup$cg`, dir, 1)
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "dyna-load 150 42") {
		t.Fatalf("heartbeat: %s: %v", out, err)
	}
}

// seqRunner answers each probe with the next body. Every tick in a test consumes one body.
// It closes drained when it returns the last body, so the driver cancels only after the final probe.
type seqRunner struct {
	bodies  []string
	drained chan struct{}
}

func (r *seqRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "-O" || a == "-M" {
			return nil, nil
		}
	}
	body := r.bodies[0]
	r.bodies = r.bodies[1:]
	if len(r.bodies) == 0 {
		close(r.drained)
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", body, len(body))), nil
}

type pressureReport struct {
	slice string
	kind  contract.PressureKind
}

// driveHeartbeat runs one probe for each tick time and returns the pressure reports in order.
func driveHeartbeat(t *testing.T, bodies []string, at []time.Time) []pressureReport {
	t.Helper()
	runner := &seqRunner{bodies: bodies, drained: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", runner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan struct{})
	var got []pressureReport
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, "/data", func(bool, Load) {}, func(slice string, kind contract.PressureKind) {
			got = append(got, pressureReport{slice, kind})
		})
	}()
	for _, now := range at {
		ticks <- now
	}
	<-runner.drained
	cancel()
	<-done
	return got
}

// tickTimes returns n ticks spaced ten seconds apart.
func tickTimes(n int) []time.Time {
	at := make([]time.Time, n)
	for i := range at {
		at[i] = time.Unix(1000+int64(10*i), 0)
	}
	return at
}

const (
	slicePathA = "/sys/fs/cgroup/dyna.slice/dyna-a.slice/memory.events.local"
	slicePathB = "/sys/fs/cgroup/dyna.slice/dyna-b.slice/memory.events.local"
)

// memoryEvents formats the counters of one memory.events.local file, as the host script prints them.
func memoryEvents(path string, high, max, oom int64) string {
	return fmt.Sprintf("%s:high %d\n%s:max %d\n%s:oom_kill %d\n", path, high, path, max, path, oom)
}

func sliceBody(high, max, oom int64) string {
	return memoryEvents(slicePathA, high, max, oom)
}

func TestHeartbeatFirstSampleOnlySetsBaseline(t *testing.T) {
	body := memoryEvents(slicePathA, 9, 4, 2) + memoryEvents(slicePathB, 9, 4, 2)
	if got := driveHeartbeat(t, []string{body}, tickTimes(1)); len(got) != 0 {
		t.Fatalf("reports = %+v, want none on the baseline read", got)
	}
}

func TestHeartbeatChildRiseReportsOnlyChild(t *testing.T) {
	// A child cgroup's own counters rise. Its parent's memory.events.local stays flat, so only the child reports.
	parent := slicePathA
	child := "/sys/fs/cgroup/dyna.slice/dyna-a.slice/dyna-a-b.slice/memory.events.local"
	bodies := []string{
		memoryEvents(parent, 0, 0, 0) + memoryEvents(child, 0, 0, 0),
		memoryEvents(parent, 0, 0, 0) + memoryEvents(child, 0, 1, 0),
	}
	got := driveHeartbeat(t, bodies, tickTimes(2))
	want := pressureReport{"dyna-a-b.slice", contract.PressureMax}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("reports = %+v, want only %+v", got, want)
	}
}

func TestHeartbeatNoSlicesIsNotAMiss(t *testing.T) {
	// Three empty probes would report unreachable after three misses. The driver fails the test on any reachability change.
	driveHeartbeat(t, []string{"", "", ""}, tickTimes(3))
}

func TestHeartbeatIgnoresContainerScopes(t *testing.T) {
	// A container scope under a box slice hits its own max. Only slice files count, so nothing reports.
	scope := "/sys/fs/cgroup/dyna.slice/dyna-a.slice/libpod-x.scope/memory.events.local"
	bodies := []string{
		memoryEvents(scope, 0, 0, 0),
		memoryEvents(scope, 0, 1, 0),
	}
	if got := driveHeartbeat(t, bodies, tickTimes(2)); len(got) != 0 {
		t.Fatalf("reports = %+v, want none for a scope", got)
	}
}

func TestHeartbeatBadSliceOutputIsNotAMiss(t *testing.T) {
	// The host answers, but a counter is not a number. Three such probes must not report unreachable.
	bad := slicePathA + ":high x\n"
	driveHeartbeat(t, []string{bad, bad, bad}, tickTimes(3))
}

func TestHeartbeatFallenCounterResetsBaseline(t *testing.T) {
	// The cgroup was created again: max fell at tick 0 and rose at tick 1. That sample only sets the baseline.
	// A later max rise, 80 s on, is a new episode and reports once.
	bodies := []string{sliceBody(9, 4, 0)}
	for range 8 {
		bodies = append(bodies, sliceBody(2, 5, 0))
	}
	bodies = append(bodies, sliceBody(2, 6, 0))
	got := driveHeartbeat(t, bodies, tickTimes(len(bodies)))
	want := pressureReport{"dyna-a.slice", contract.PressureMax}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("reports = %+v, want only %+v", got, want)
	}
}

func TestHeartbeatMissingSliceResetsBaseline(t *testing.T) {
	// Slice A is gone for one probe. When it returns with a higher max, that sample only sets the baseline.
	bodies := []string{sliceBody(0, 4, 0), memoryEvents(slicePathB, 0, 0, 0), sliceBody(0, 5, 0)}
	if got := driveHeartbeat(t, bodies, tickTimes(len(bodies))); len(got) != 0 {
		t.Fatalf("reports = %+v, want none after the slice returns", got)
	}
}

func TestHeartbeatEpisodes(t *testing.T) {
	cases := []struct {
		name   string
		ticks  int
		counts func(int) (high, max, oom int64)
		want   int
	}{
		{"sixty second high rise reports once", 9, func(i int) (int64, int64, int64) { return int64(i), 0, 0 }, 1},
		{"short high rise does not report", 12, func(i int) (int64, int64, int64) { return int64(min(i, 5)), 0, 0 }, 0},
		{"second high episode after a quiet minute", 22, func(i int) (int64, int64, int64) {
			high := min(i, 7)
			if i > 13 {
				high += i - 13
			}
			return int64(high), 0, 0
		}, 2},
		{"max rise reports once per episode", 21, func(i int) (int64, int64, int64) {
			switch {
			case i == 20:
				return 0, 3, 0
			case i > 2:
				return 0, 2, 0
			}
			return 0, int64(i), 0
		}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bodies := make([]string, c.ticks)
			for i := range bodies {
				high, max, oom := c.counts(i)
				bodies[i] = sliceBody(high, max, oom)
			}
			if got := driveHeartbeat(t, bodies, tickTimes(c.ticks)); len(got) != c.want {
				t.Fatalf("got %d reports, want %d: %+v", len(got), c.want, got)
			}
		})
	}
}

func TestHeartbeatReportsLoad(t *testing.T) {
	runner := &seqRunner{bodies: []string{"dyna-disk 1000 400\ndyna-load 100 1000000\n", "dyna-load 250 16000000\n"}, drained: make(chan struct{})}
	h, err := New("user@host", t.TempDir(), "host", runner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	type sample struct {
		reachable           bool
		cpu                 float64
		mem                 int64
		diskBytes, diskUsed int64
	}
	reports := make(chan sample)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Heartbeat(ctx, ticks, "/data", func(ok bool, load Load) {
			reports <- sample{ok, load.CPUUsed, load.MemUsed, load.DiskBytes, load.DiskUsed}
		}, func(string, contract.PressureKind) {})
	}()
	for i, want := range []sample{{true, 0, 100, 1000, 400}, {true, 1.5, 250, 0, 0}} {
		ticks <- time.Unix(1000+int64(10*i), 0)
		if got := <-reports; got != want {
			t.Errorf("sample %d: got %+v, want %+v", i, got, want)
		}
	}
	cancel()
	<-done
}

func TestHeartbeatDiskFailureKeepsProbeAlive(t *testing.T) {
	script := `df() { return 1; }; systemctl() { :; }; ` + heartbeatScript
	out, err := exec.Command("bash", "-c", script, "dyna", "/missing").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "dyna-load 0 0") {
		t.Fatalf("probe output %q, error %v", out, err)
	}
}

const gib = int64(1) << 30

// btrfsTree writes one filesystem under root: 1000 GiB allocated, metadata chunks with chunkFree left, and devices with unalloc more.
func btrfsTree(t *testing.T, root string, chunkFree, unalloc int64) {
	t.Helper()
	write := func(name string, value int64) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", value)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("allocation/metadata/total_bytes", 50*gib)
	write("allocation/metadata/bytes_used", 50*gib-chunkFree)
	write("allocation/metadata/disk_total", 50*gib)
	write("allocation/data/disk_total", 949*gib)
	write("allocation/system/disk_total", gib)
	write("devices/vda1/size", (1000*gib+unalloc)/512)
}

// diskProbe runs the shared snippet with df reporting size 1000 and 600 available.
func diskProbe(t *testing.T, sysfs, fstype, uuid, source string) string {
	t.Helper()
	shim := fmt.Sprintf(`df() { printf 'size avail\n1000 600\n'; }
findmnt() { case "$*" in *FSTYPE*) echo %q ;; *UUID*) echo %q ;; *SOURCE*) echo %q ;; esac; }
`, fstype, uuid, source)
	out, err := exec.Command("bash", "-c", shim+strings.ReplaceAll(diskScript, "/sys/fs/btrfs", sysfs), "dyna", "/data").CombinedOutput()
	if err != nil {
		t.Fatalf("probe: %s: %v", out, err)
	}
	return strings.TrimSpace(string(out))
}

func TestDiskScript(t *testing.T) {
	const plain, full = "dyna-disk 1000 400", "dyna-disk 1000 1000"
	remove := func(name string) func(string) error {
		return func(dir string) error { return os.Remove(filepath.Join(dir, name)) }
	}
	for _, tc := range []struct {
		name               string
		chunkFree, unalloc int64
		fstype, uuid       string
		source             string
		change             func(string) error
		want               string
	}{
		{name: "not btrfs ignores metadata", fstype: "ext4", want: plain},
		{name: "half of unallocated counts", chunkFree: gib, unalloc: 4 * gib, want: plain},
		{name: "two GiB left is enough", chunkFree: gib, unalloc: 2 * gib, want: plain},
		{name: "under two GiB left", chunkFree: gib, unalloc: 2*gib - 1024, want: full},
		{name: "no metadata room", want: full},
		{name: "allocation past the devices counts no unallocated", chunkFree: 3 * gib, unalloc: -100 * gib, want: plain},
		{name: "uuid lookup fails, device finds it", uuid: "none", want: full},
		{name: "uuid and device lookups fail", uuid: "none", source: "none", want: plain},
		{name: "metadata unreadable", change: remove("fsid/allocation/metadata/bytes_used"), want: plain},
		{name: "allocation unreadable", change: remove("fsid/allocation/system/disk_total"), want: plain},
		{name: "one device unreadable", change: func(dir string) error { return os.Mkdir(filepath.Join(dir, "fsid/devices/vdb1"), 0700) }, want: plain},
		{name: "sysfs missing", change: func(dir string) error { return os.RemoveAll(filepath.Join(dir, "fsid")) }, want: plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			btrfsTree(t, filepath.Join(dir, "fsid"), tc.chunkFree, tc.unalloc)
			if tc.change != nil {
				if err := tc.change(dir); err != nil {
					t.Fatal(err)
				}
			}
			fstype, uuid, source := "btrfs", "fsid", "/dev/vda1[/@]"
			if tc.fstype != "" {
				fstype = tc.fstype
			}
			if tc.uuid != "" {
				uuid = ""
			}
			if tc.source != "" {
				source = ""
			}
			if got := diskProbe(t, dir, fstype, uuid, source); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiskScriptPicksTheRightFilesystemByDevice(t *testing.T) {
	dir := t.TempDir()
	btrfsTree(t, filepath.Join(dir, "aaa"), 10*gib, 0)
	if err := os.Rename(filepath.Join(dir, "aaa/devices/vda1"), filepath.Join(dir, "aaa/devices/vdb1")); err != nil {
		t.Fatal(err)
	}
	btrfsTree(t, filepath.Join(dir, "fsid"), 0, 0)
	if got := diskProbe(t, dir, "btrfs", "", "/dev/vda1[/@]"); got != "dyna-disk 1000 1000" {
		t.Fatalf("got %q", got)
	}
}

func TestParseDisk(t *testing.T) {
	for out, want := range map[string][2]int64{
		"dyna-disk 1000 400\ndyna-load 1 2\n":                 {1000, 400},
		"dyna-load 1 2\ndyna-disk 1000 1000\n/slice:high 3\n": {1000, 1000},
		"dyna-load 1 2\n":        {0, 0},
		"dyna-disk 1000\n":       {0, 0},
		"dyna-disk size avail\n": {0, 0},
		"":                       {0, 0},
	} {
		if total, used := parseDisk([]byte(out)); total != want[0] || used != want[1] {
			t.Errorf("%q: got %d %d", out, total, used)
		}
	}
}

func TestLocalDisk(t *testing.T) {
	dir := t.TempDir()
	total, used := LocalDisk(context.Background(), dir)
	if total <= 0 || used < 0 || used > total {
		t.Fatalf("disk %d of %d", used, total)
	}
	if total, used := LocalDisk(context.Background(), filepath.Join(dir, "missing")); total != 0 || used != 0 {
		t.Fatalf("missing path reported %d of %d", used, total)
	}
}
