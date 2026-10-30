package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	r, err := Linux{ProcDir: proc}.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.MemTotal != 32000000<<10 || r.MemAvail != 20000000<<10 || r.PressureMem != 12.25 {
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
	r, err := Linux{ProcDir: proc}.Read(context.Background())
	if err != nil || r.PressureMem != -1 || r.MemAvail != 300<<10 {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

func TestLinuxNarrowsMemoryToACgroupV2Limit(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "0::/user.slice/app.slice\n",
		"self/mountinfo": "30 25 0:26 / " + cg + " rw,nosuid - cgroup2 cgroup2 rw\n",
	})
	put(t, cg, map[string]string{
		"user.slice/app.slice/memory.max":     "max\n",
		"user.slice/app.slice/memory.current": "100\n",
		"user.slice/memory.max":               "8589934592\n", // an 8 GiB slice
		"user.slice/memory.current":           "2147483648\n", // 2 GiB used
	})
	r, err := Linux{ProcDir: proc}.Read(context.Background())
	if err != nil || r.MemTotal != 8<<30 || r.MemAvail != 6<<30 || !strings.HasSuffix(r.Source, "cgroup v2") {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

// A container or delegated slice mounts only a subtree: the mount's root is
// /delegated, and the process lives at /delegated/runner. The limit sits in
// <mount>/runner, not <mount>/delegated/runner.
func TestLinuxFindsALimitUnderASubtreeMount(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "0::/delegated/runner\n",
		"self/mountinfo": "30 25 0:26 /delegated " + cg + " rw - cgroup2 cgroup2 rw\n",
	})
	put(t, cg, map[string]string{"runner/memory.max": "1073741824\n", "runner/memory.current": "268435456\n"})
	r, err := Linux{ProcDir: proc}.Read(context.Background())
	if err != nil || r.MemTotal != 1<<30 || r.MemAvail != 768<<20 {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

func TestLinuxNarrowsMemoryToACgroupV1Limit(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	// The memory controller is mounted at a custom place, with cpu beside it.
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "4:cpu,cpuacct:/docker/abc\n3:memory:/docker/abc\n",
		"self/mountinfo": "40 30 0:30 / " + cg + "/mem rw - cgroup cgroup rw,cpu,memory\n",
	})
	put(t, cg, map[string]string{
		"mem/docker/abc/memory.limit_in_bytes": "4294967296\n",
		"mem/docker/abc/memory.usage_in_bytes": "1073741824\n",
		"mem/docker/memory.limit_in_bytes":     "9223372036854771712\n", // no limit
	})
	r, err := Linux{ProcDir: proc}.Read(context.Background())
	if err != nil || r.MemTotal != 4<<30 || r.MemAvail != 3<<30 || !strings.HasSuffix(r.Source, "cgroup v1") {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

// A limit with no readable usage is a partial reading: it fails, so the gate
// lets turns through, instead of reporting the whole limit as free.
func TestLinuxFailsOnALimitWithoutUsage(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "0::/a\n",
		"self/mountinfo": "30 25 0:26 / " + cg + " rw - cgroup2 cgroup2 rw\n",
	})
	put(t, cg, map[string]string{"a/memory.max": "1073741824\n"})
	if _, err := (Linux{ProcDir: proc}).Read(context.Background()); err == nil {
		t.Fatal("a limit with no usage read as fully free")
	}
}

func TestIncompleteMemoryOutputIsAnError(t *testing.T) {
	if _, _, err := ParseMeminfo([]byte("MemTotal: 1000 kB\n")); err == nil {
		t.Error("meminfo with no available figure parsed")
	}
	if _, _, err := ParseMeminfo([]byte("MemTotal: 1000 kB\nMemFree: 1 kB\nBuffers: 1 kB\n")); err == nil {
		t.Error("meminfo with part of the fallback parsed")
	}
	if _, err := ParseVMStat([]byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages active: 5.\n")); err == nil {
		t.Error("vm_stat with no free count parsed")
	}
	if _, err := ParseVMStat([]byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: lots.\nPages inactive: 1.\n")); err == nil {
		t.Error("vm_stat with a bad count parsed")
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
	if r.MemTotal != 16<<30 || r.MemAvail != 36000*16384 || r.PressureMem != -1 {
		t.Fatalf("readings = %+v", r)
	}
}

func TestDarwinFailsWhenACommandFails(t *testing.T) {
	d := Darwin{Run: func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }}
	if _, err := d.Read(context.Background()); err == nil {
		t.Fatal("a failing sysctl read as healthy")
	}
}

func TestLinuxUsesTheMountThatShowsTheProcess(t *testing.T) {
	proc, cg1, cg2 := t.TempDir(), t.TempDir(), t.TempDir()
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "0::/../outer/inner\n",
		"self/mountinfo": "30 25 0:26 /elsewhere " + cg1 + " rw - cgroup2 cgroup2 rw\n31 25 0:27 /../outer " + cg2 + " rw - cgroup2 cgroup2 rw\n",
	})
	put(t, cg2, map[string]string{"inner/memory.max": "2147483648\n", "inner/memory.current": "0\n"})
	r, err := Linux{ProcDir: proc}.Read(context.Background())
	if err != nil || r.MemTotal != 2<<30 {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

// The process sits at /../outer/inner, outside what a mount of the whole
// hierarchy shows. Its limit is unknown, and the host's memory must not stand in
// for it: the reading fails.
func TestLinuxFailsWhenTheCgroupIsOutsideEveryMount(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo, "self/cgroup": "0::/../outer/inner\n",
		"self/mountinfo": "30 25 0:26 / " + cg + " rw - cgroup2 cgroup2 rw\n",
	})
	if r, err := (Linux{ProcDir: proc}).Read(context.Background()); err == nil {
		t.Fatalf("read host memory %d for an unresolvable cgroup", r.MemTotal)
	}
}

// The engine runs in app.slice and the agents in dyna.slice, beside it under
// the same user manager. The agents' slice has the tighter cap, and its usage
// counts, even though the engine's own cgroup has none.
func TestLinuxReadsTheAgentSlicesBudgetBesideTheEngine(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	user := "user.slice/user-1000.slice/user@1000.service"
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo,
		"self/cgroup":    "0::/" + user + "/app.slice/dyna-engine.service\n",
		"self/mountinfo": "30 25 0:26 / " + cg + " rw - cgroup2 cgroup2 rw\n",
	})
	put(t, cg, map[string]string{
		user + "/app.slice/dyna-engine.service/memory.max": "max\n",
		user + "/dyna.slice/memory.max":                    "25769803776\n", // 24 GiB: 80% of a 30 GiB host
		user + "/dyna.slice/memory.current":                "26214400000\n", // already past most of it
	})
	r, err := Linux{ProcDir: proc}.Read(context.Background())
	if err != nil || r.MemTotal != 25769803776 || r.MemAvail != 0 {
		t.Fatalf("readings = %+v, %v", r, err)
	}
}

// A mount of only the engine's service subtree cannot show dyna.slice beside
// it. The agents' budget is then unknown, so the reading fails instead of
// reporting the host's memory.
func TestLinuxFailsWhenASubtreeMountHidesTheAgentSlice(t *testing.T) {
	proc, cg := t.TempDir(), t.TempDir()
	user := "/user.slice/user-1000.slice/user@1000.service"
	put(t, proc, map[string]string{
		"loadavg": "0.1 0 0 1/1 1\n", "meminfo": meminfo,
		"self/cgroup":    "0::" + user + "/app.slice/dyna-engine.service\n",
		"self/mountinfo": "30 25 0:26 " + user + "/app.slice/dyna-engine.service " + cg + " rw - cgroup2 cgroup2 rw\n",
	})
	put(t, cg, map[string]string{"memory.max": "max\n"})
	if r, err := (Linux{ProcDir: proc}).Read(context.Background()); err == nil {
		t.Fatalf("read host memory %d with the agents' slice invisible", r.MemTotal)
	}
}
