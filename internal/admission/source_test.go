package admission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// put writes fixture files under root, creating directories as needed.
func put(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const meminfo = "MemTotal:       32000000 kB\nMemFree:         1000000 kB\nMemAvailable:   20000000 kB\nBuffers:          500000 kB\nCached:          4000000 kB\n"

func TestLinuxReadsProcAndPressure(t *testing.T) {
	proc := t.TempDir()
	put(t, proc, map[string]string{
		"loadavg":         "1.50 1.20 1.00 2/300 12345\n",
		"meminfo":         meminfo,
		"pressure/cpu":    "some avg10=3.50 avg60=1.00 avg300=0.50 total=1\n",
		"pressure/memory": "some avg10=12.25 avg60=4.00 avg300=1.00 total=1\nfull avg10=9.00 avg60=0 avg300=0 total=0\n",
	})
	r, err := Linux{ProcDir: proc, CgroupDir: filepath.Join(proc, "cg")}.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Load1 != 1.5 || r.MemTotal != 32000000<<10 || r.MemAvail != 20000000<<10 || r.PressureCPU != 3.5 || r.PressureMem != 12.25 {
		t.Fatalf("readings = %+v", r)
	}
	if r.Source != "linux /proc + PSI" {
		t.Fatalf("source = %q", r.Source)
	}
}

// A kernel without PSI or MemAvailable still reads, and says -1 for pressure.
func TestLinuxFallsBackWithoutPSIAndMemAvailable(t *testing.T) {
	proc := t.TempDir()
	put(t, proc, map[string]string{
		"loadavg": "0.10 0.10 0.10 1/100 1\n",
		"meminfo": "MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 150 kB\n",
	})
	r, err := Linux{ProcDir: proc, CgroupDir: proc}.Read(context.Background())
	if err != nil || r.PressureCPU != -1 || r.PressureMem != -1 || r.MemAvail != 300<<10 {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

func TestLinuxNarrowsMemoryToACgroupV2Limit(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	put(t, proc, map[string]string{"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "0::/user.slice/app.slice\n"})
	put(t, cg, map[string]string{
		"user.slice/app.slice/memory.max":     "max\n",
		"user.slice/app.slice/memory.current": "100\n",
		"user.slice/memory.max":               "8589934592\n", // an 8 GiB slice
		"user.slice/memory.current":           "2147483648\n", // 2 GiB used
	})
	r, err := Linux{ProcDir: proc, CgroupDir: cg}.Read(context.Background())
	if err != nil || r.MemTotal != 8<<30 || r.MemAvail != 6<<30 || !strings.HasSuffix(r.Source, "cgroup v2") {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

func TestLinuxNarrowsMemoryToACgroupV1Limit(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	put(t, proc, map[string]string{"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "4:cpu:/docker/abc\n3:memory:/docker/abc\n"})
	put(t, cg, map[string]string{
		"memory/docker/abc/memory.limit_in_bytes": "4294967296\n",
		"memory/docker/abc/memory.usage_in_bytes": "1073741824\n",
		"memory/docker/memory.limit_in_bytes":     "9223372036854771712\n", // no limit
	})
	r, err := Linux{ProcDir: proc, CgroupDir: cg}.Read(context.Background())
	if err != nil || r.MemTotal != 4<<30 || r.MemAvail != 3<<30 || !strings.HasSuffix(r.Source, "cgroup v1") {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

func TestLinuxFailsOnAMissingProc(t *testing.T) {
	if _, err := (Linux{ProcDir: t.TempDir()}).Read(context.Background()); err == nil {
		t.Fatal("a machine with no /proc files read as healthy")
	}
}

const vmStat = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10000.
Pages active:                            500000.
Pages inactive:                           20000.
Pages speculative:                         5000.
Pages purgeable:                           1000.
`

func TestDarwinReadsSysctlAndVMStat(t *testing.T) {
	d := Darwin{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name + " " + strings.Join(args, " ") {
		case "sysctl -n hw.memsize":
			return []byte("17179869184\n"), nil
		case "vm_stat ":
			return []byte(vmStat), nil
		case "sysctl -n vm.loadavg":
			return []byte("{ 2.25 1.50 1.10 }\n"), nil
		}
		t.Errorf("unexpected command %s %v", name, args)
		return nil, os.ErrNotExist
	}}
	r, err := d.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.MemTotal != 16<<30 || r.MemAvail != 36000*16384 || r.Load1 != 2.25 || r.PressureMem != -1 {
		t.Fatalf("readings = %+v", r)
	}
}

func TestDarwinFailsWhenACommandFails(t *testing.T) {
	d := Darwin{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }}
	if _, err := d.Read(context.Background()); err == nil {
		t.Fatal("a failing sysctl read as healthy")
	}
}

func TestMeasureTurnsSumsTheRunnersProcessTree(t *testing.T) {
	proc := t.TempDir()
	// Runner 100 has children 101 and 102 (a command name with a space and a
	// parenthesis must not break the parse); runner 200 stands alone.
	put(t, proc, map[string]string{
		"100/stat": "100 (dyna) S 1 0\n", "100/statm": "0 1000 0 0 0 0 0\n",
		"101/stat": "101 (claude code (x)) S 100 0\n", "101/statm": "0 3000 0 0 0 0 0\n",
		"102/stat": "102 (sh) S 100 0\n", "102/statm": "0 500 0 0 0 0 0\n",
		"200/stat": "200 (dyna) S 1 0\n", "200/statm": "0 2000 0 0 0 0 0\n",
		"300/stat": "300 (other) S 1 0\n", "300/statm": "0 99999 0 0 0 0 0\n",
	})
	// (1000+3000+500 + 2000) / 2 runners, 4096 bytes a page.
	if got, want := MeasureTurns(proc, []int{100, 200}, 4096), uint64(6500*4096/2); got != want {
		t.Fatalf("measured %d, want %d", got, want)
	}
	if got := MeasureTurns(proc, []int{999}, 4096); got != 0 {
		t.Fatalf("a runner that is gone measured %d, want 0 so the fallback stays", got)
	}
}

func TestObservationsSummarizeTheLastDay(t *testing.T) {
	o := Observed{DataDir: t.TempDir()}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, ob := range []Observation{
		{At: now.Add(-2 * time.Hour), Pool: Turns, Room: "a", Held: 7, Limit: 5},
		{At: now.Add(-time.Hour), Pool: Turns, Room: "b", Held: 9, Limit: 5},
		{At: now.Add(-30 * time.Hour), Pool: Turns, Room: "a", Held: 20, Limit: 5}, // too old
	} {
		if err := o.Record(ob); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := o.Summarize(now.Add(-24 * time.Hour))
	if err != nil || sum.Turns != 2 || sum.MostOver != 5 {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
}
