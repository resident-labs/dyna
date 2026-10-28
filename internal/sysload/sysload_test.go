package sysload

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const um = "user.slice/user-1000.slice/user@1000.service"

type fakeTree struct {
	t          *testing.T
	proc, cgrp string
	cpus       int
	jiffies    uint64
}

func newTree(t *testing.T, withCgroups bool) *fakeTree {
	f := &fakeTree{t: t, proc: t.TempDir(), cgrp: t.TempDir()}
	f.write(f.proc, "loadavg", "59.30 45.10 32.10 3/900 12345\n")
	f.write(f.proc, "meminfo", "MemTotal:       16777216 kB\nMemFree: 1 kB\nMemAvailable:    4194304 kB\n")
	if withCgroups {
		f.write(f.proc, "self/cgroup", "0::/"+um+"/dyna.slice/dyna-ses_a-00000000.scope\n")
		f.mem("dyna.slice", 3000, 1000)
		f.mem("app.slice/dyna-engine.service", 500, 100)
		f.mem("app.slice/dyna-serve.service", 200, 50)
		f.cpu("dyna.slice", 0)
	}
	return f
}

func (f *fakeTree) write(root, name, body string) {
	f.t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// cpu sets usage_usec of a cgroup under the user manager.
func (f *fakeTree) cpu(rel string, usec uint64) {
	f.write(f.cgrp, filepath.Join(um, rel, "cpu.stat"), fmt.Sprintf("usage_usec %d\nuser_usec 1\n", usec))
}

func (f *fakeTree) mem(rel string, current, inactive uint64) {
	f.write(f.cgrp, filepath.Join(um, rel, "memory.current"), fmt.Sprintf("%d\n", current))
	f.write(f.cgrp, filepath.Join(um, rel, "memory.stat"), fmt.Sprintf("anon 1\ninactive_file %d\n", inactive))
}

// busy sets the cpu line so that busy jiffies are as given; idle and iowait
// are large and must not count. It adds one cpuN line per machine CPU.
func (f *fakeTree) busy(jiffies uint64) {
	f.jiffies = jiffies
	body := fmt.Sprintf("cpu  %d 0 0 99999 88888 0 0 0 777 777\n", jiffies)
	for i := 0; i < f.cpus; i++ {
		body += fmt.Sprintf("cpu%d 1 1 1 1 1\n", i)
	}
	f.write(f.proc, "stat", body+"intr 5\n")
}

// sampler returns a sampler of a machine with the given CPU count.
func (f *fakeTree) sampler(cpus int) *Sampler {
	f.cpus = cpus
	f.busy(f.jiffies)
	return &Sampler{procRoot: f.proc, cgroupRoot: f.cgrp, logf: func(string, ...any) {}}
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestSplitTwoSamples(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	f.cpu("dyna.slice", 0)
	f.cpu("dyna.slice/dyna-computers.service", 0)
	f.cpu("app.slice/dyna-engine.service", 0)
	f.cpu("app.slice/dyna-serve.service", 0)
	s := f.sampler(4)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	// 10 s on 4 CPUs: 40 core-seconds are the whole machine.
	f.busy(2000)                                          // 20 core-s = 50%
	f.cpu("app.slice/dyna-engine.service", 2_000_000)     // 2 s = 5%
	f.cpu("app.slice/dyna-serve.service", 2_000_000)      // 2 s = 5%
	f.cpu("dyna.slice", 10_000_000)                       // 10 s, includes computers
	f.cpu("dyna.slice/dyna-computers.service", 4_000_000) // 4 s = 10%
	if err := s.sample(t0.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	l := s.Load()
	if !l.DynaKnown || l.CPUPercent == nil || l.Split == nil {
		t.Fatalf("load = %+v", l)
	}
	near(t, "cpu", *l.CPUPercent, 50)
	near(t, "engine", l.Split.Engine, 10)
	near(t, "computers", l.Split.Computers, 10)
	near(t, "agents", l.Split.Agents, 15) // (10-4) s = 6 core-s = 15%
	near(t, "other", l.Split.Other, 15)
	if l.CPUs != 4 || l.Load != [3]float64{59.3, 45.1, 32.1} {
		t.Fatalf("cpus/load = %d %v", l.CPUs, l.Load)
	}
}

// A room computer runs in its own scope below its room's slice, outside the
// service cgroup; the computers split still counts its CPU.
func TestRoomComputerScopeCountsAsComputers(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	f.cpu("dyna.slice", 0)
	f.cpu("dyna.slice/dyna-computers.service", 0)
	scope := "dyna.slice/dyna-room.slice/dyna-room-fl.slice/dyna-computer-one-1a2b3c4d.scope"
	f.cpu(scope, 0)
	s := f.sampler(4)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	f.busy(1000)                    // 10 core-s = 25%
	f.cpu("dyna.slice", 10_000_000) // 10 s, all of it the room computer
	f.cpu(scope, 10_000_000)        // 10 s
	if err := s.sample(t0.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	l := s.Load()
	if l.Split == nil {
		t.Fatalf("load = %+v", l)
	}
	near(t, "computers", l.Split.Computers, 25)
	near(t, "agents", l.Split.Agents, 0)
}

// A room computer that exits takes its lifetime counter with it. The computers
// that stay busy must still show their own use, not drop to zero.
func TestExitedRoomComputerDoesNotHideTheBusyOne(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	slice := "dyna.slice/dyna-room.slice/dyna-room-fl.slice/"
	gone, busy := slice+"dyna-computer-gone-1.scope", slice+"dyna-computer-busy-2.scope"
	f.cpu("dyna.slice", 0)
	f.cpu(gone, 100_000_000) // 100 s of history
	f.cpu(busy, 0)
	s := f.sampler(4)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.cgrp, um, gone)); err != nil {
		t.Fatal(err)
	}
	f.busy(1000)
	f.cpu("dyna.slice", 10_000_000)
	f.cpu(busy, 10_000_000) // 10 s of 40 core-s = 25%
	if err := s.sample(t0.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	l := s.Load()
	if l.Split == nil {
		t.Fatalf("load = %+v", l)
	}
	near(t, "computers", l.Split.Computers, 25)
	near(t, "agents", l.Split.Agents, 0)
}

func TestFirstSampleHasNoCPUShare(t *testing.T) {
	f := newTree(t, true)
	f.busy(100)
	s := f.sampler(2)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	l := s.Load()
	if l.CPUPercent != nil || l.Split != nil || l.Top != nil {
		t.Fatalf("first sample reported a CPU share: %+v", l)
	}
	if l.Load[0] != 59.3 || l.Memory.Total == 0 || !l.DynaKnown {
		t.Fatalf("first sample lost load or memory: %+v", l)
	}
}

func TestTopSumsScopesAndOrders(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	names := map[string][2]uint64{ // scope -> usec before, after (10 s apart)
		"dyna-ses_a-aaaaaaaa.scope": {0, 10_000_000}, // 1 core
		"dyna-ses_a-bbbbbbbb.scope": {0, 10_000_000}, // 1 core: ses_a = 2 cores
		"dyna-wf_x-cccccccc.scope":  {0, 10_000_000},
		"dyna-ses_b-dddddddd.scope": {0, 10_000_000}, // ties with wf_x; ses_b first
		"dyna-ses_z-eeeeeeee.scope": {0, 5_000_000},
		"dyna-ses_q-ffffffff.scope": {0, 0},
	}
	s := f.sampler(8)
	for i, at := range []time.Time{t0, t0.Add(10 * time.Second)} {
		f.busy(uint64(i) * 100)
		for n, v := range names {
			f.cpu("dyna.slice/"+n, v[i])
		}
		if err := s.sample(at); err != nil {
			t.Fatal(err)
		}
	}
	top := s.Load().Top
	want := []Top{{"ses_a", 2}, {"ses_b", 1}, {"wf_x", 1}}
	if len(top) != len(want) {
		t.Fatalf("top = %+v", top)
	}
	for i := range want {
		if top[i].ID != want[i].ID {
			t.Fatalf("top = %+v, want %+v", top, want)
		}
		near(t, top[i].ID, top[i].Cores, want[i].Cores)
	}
}

func TestGoneScopeIsNeverNegative(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	f.cpu("dyna.slice", 9_000_000)
	f.cpu("dyna.slice/dyna-ses_a-aaaaaaaa.scope", 9_000_000)
	s := f.sampler(2)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.cgrp, um, "dyna.slice/dyna-ses_a-aaaaaaaa.scope")); err != nil {
		t.Fatal(err)
	}
	f.cpu("dyna.slice", 1_000_000) // the slice counter fell too
	f.busy(0)
	if err := s.sample(t0.Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	l := s.Load()
	if l.Split == nil || l.Split.Agents != 0 || l.Split.Other < 0 || len(l.Top) != 0 {
		t.Fatalf("load = %+v split=%+v", l, l.Split)
	}
}

func TestMemorySubtractsInactiveFile(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	s := f.sampler(2)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	m := s.Load().Memory
	if m.Total != 16<<30 || m.Used != 12<<30 {
		t.Fatalf("machine memory = %+v", m)
	}
	// (3000-1000) + (500-100) + (200-50)
	if m.Dyna != 2550 {
		t.Fatalf("dyna memory = %d, want 2550", m.Dyna)
	}
}

func TestMissingCgroupTree(t *testing.T) {
	f := newTree(t, false)
	f.busy(0)
	s := f.sampler(2)
	for i, at := range []time.Time{t0, t0.Add(5 * time.Second)} {
		f.busy(uint64(i) * 500)
		if err := s.sample(at); err != nil {
			t.Fatal(err)
		}
	}
	l := s.Load()
	if l.DynaKnown || l.Split != nil || l.Top != nil || l.Memory.Dyna != 0 {
		t.Fatalf("load = %+v", l)
	}
	if l.CPUPercent == nil || l.Load[0] != 59.3 || l.Memory.Total == 0 {
		t.Fatalf("load, CPU and memory must still show: %+v", l)
	}
	near(t, "cpu", *l.CPUPercent, 50) // 5 core-s of 10 core-s
}

func TestScopeID(t *testing.T) {
	for name, want := range map[string]string{
		"dyna-ses_20261019-191933-6c72194e-de56b8ba.scope": "ses_20261019-191933-6c72194e",
		"dyna-wf_ok-1-0a0b0c0d.scope":                      "wf_ok-1",
		"dyna-0a0b0c0d.scope":                              "dyna-0a0b0c0d",
	} {
		if got := scopeID(name); got != want {
			t.Errorf("scopeID(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRepeatedFailureIsLoggedOnce(t *testing.T) {
	f := newTree(t, false)
	var logged []string
	s := &Sampler{procRoot: f.proc, cgroupRoot: f.cgrp}
	s.logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	s.tick(t0) // no stat file yet
	s.tick(t0.Add(Interval))
	if len(logged) != 1 {
		t.Fatalf("two identical failures logged %d lines: %q", len(logged), logged)
	}
	f.busy(0)
	s.tick(t0.Add(2 * Interval))
	os.Remove(filepath.Join(f.proc, "stat"))
	s.tick(t0.Add(3 * Interval))
	if len(logged) != 2 {
		t.Fatalf("a failure after a success must log again: %q", logged)
	}
}

func TestCPUCountIsTheMachineNotTheProcess(t *testing.T) {
	f := newTree(t, false)
	f.busy(0)
	s := f.sampler(16) // far above the CPUs this test process may use
	for i, at := range []time.Time{t0, t0.Add(10 * time.Second)} {
		f.busy(uint64(i) * 8000) // 80 core-s in 10 s on 16 CPUs = 50%
		if err := s.sample(at); err != nil {
			t.Fatal(err)
		}
	}
	l := s.Load()
	if l.CPUs != 16 || l.CPUPercent == nil {
		t.Fatalf("load = %+v", l)
	}
	near(t, "cpu", *l.CPUPercent, 50)
}

func TestSplitWaitsForTwoReadingsOfTheCgroupTree(t *testing.T) {
	f := newTree(t, false) // no dyna.slice yet
	f.busy(0)
	s := f.sampler(2)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	// The tree appears holding usage built up long before this sample.
	f.write(f.proc, "self/cgroup", "0::/"+um+"/dyna.slice/dyna-ses_a-00000000.scope\n")
	f.cpu("dyna.slice", 900_000_000)
	f.cpu("dyna.slice/dyna-ses_a-aaaaaaaa.scope", 900_000_000)
	f.cpu("app.slice/dyna-engine.service", 900_000_000)
	f.busy(500)
	if err := s.sample(t0.Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	l := s.Load()
	if !l.DynaKnown || l.Split != nil || l.Top != nil || l.CPUPercent == nil {
		t.Fatalf("lifetime usage was shown as one interval: %+v split=%+v", l, l.Split)
	}
	f.cpu("dyna.slice", 901_000_000)
	f.cpu("dyna.slice/dyna-ses_a-aaaaaaaa.scope", 901_000_000)
	f.busy(1000)
	if err := s.sample(t0.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	l = s.Load()
	if l.Split == nil || len(l.Top) != 1 {
		t.Fatalf("two cgroup readings must give a split: %+v", l)
	}
	near(t, "agents", l.Split.Agents, 10) // 1 core-s in 5 s on 2 CPUs
}

func TestSplitWaitsAfterAFailedCounterRead(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	f.cpu("dyna.slice", 1_000_000)
	s := f.sampler(2)
	if err := s.sample(t0); err != nil {
		t.Fatal(err)
	}
	// The directory stays, but its counter file cannot be read.
	stat := filepath.Join(f.cgrp, um, "dyna.slice", "cpu.stat")
	os.Remove(stat)
	if err := os.Mkdir(stat, 0o755); err != nil {
		t.Fatal(err)
	}
	f.busy(500)
	if err := s.sample(t0.Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// It recovers holding 900 s of usage built up while unread.
	os.Remove(stat)
	f.cpu("dyna.slice", 900_000_000)
	f.busy(1000)
	if err := s.sample(t0.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if l := s.Load(); l.Split != nil || l.Top != nil || !l.DynaKnown {
		t.Fatalf("lifetime usage was shown as one interval: %+v split=%+v", l, l.Split)
	}
	f.cpu("dyna.slice", 901_000_000)
	f.busy(1500)
	if err := s.sample(t0.Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if l := s.Load(); l.Split == nil {
		t.Fatalf("two valid readings must give a split: %+v", l)
	}
}

func TestGroupsHoldMemoryIOAndPressure(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	f.write(f.proc, "pressure/cpu", "some avg10=12.50 avg60=3.00 avg300=1.00 total=9\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	f.write(f.proc, "pressure/memory", "some avg10=4.00 avg60=0 avg300=0 total=1\n")
	scope := "dyna.slice/dyna-ses_a-aaaaaaaa.scope"
	f.mem(scope, 900, 100)
	f.write(f.cgrp, filepath.Join(um, scope, "memory.pressure"), "some avg10=30.25 avg60=0 avg300=0 total=1\n")
	s := f.sampler(2)
	for i, at := range []time.Time{t0, t0.Add(10 * time.Second)} {
		f.busy(uint64(i) * 1000) // 10 core-s in 10 s = 1 core
		f.cpu("dyna.slice", uint64(i)*5_000_000)
		f.cpu(scope, uint64(i)*5_000_000)
		f.write(f.cgrp, filepath.Join(um, scope, "io.stat"), fmt.Sprintf("8:0 rbytes=%d wbytes=%d rios=1\n259:0 rbytes=%d wbytes=0\n", i*1000, i*2000, i*7000))
		if err := s.sample(at); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]Group{}
	for _, g := range s.Load().Groups {
		got[g.Name] = g
	}
	m := got["machine"]
	near(t, "machine cores", m.Cores, 1)
	near(t, "machine cpu pressure", m.Pressure.CPU, 12.5)
	near(t, "machine memory pressure", m.Pressure.Memory, 4)
	if m.Memory != 12<<30 {
		t.Fatalf("machine memory = %d", m.Memory)
	}
	ses := got["session:ses_a"]
	near(t, "session cores", ses.Cores, 0.5)
	near(t, "session io", ses.IO, 1000) // 10000 bytes in 10 s
	near(t, "session memory pressure", ses.Pressure.Memory, 30.25)
	if ses.Memory != 800 {
		t.Fatalf("session memory = %d, want 800", ses.Memory)
	}
	if a := got["agents"]; a.Memory != 2000 {
		t.Fatalf("agents = %+v", a)
	}
	for _, name := range []string{"engine", "computers"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("no %s group in %+v", name, got)
		}
	}
}

func TestRecordGetsMinuteAverages(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	s := f.sampler(2)
	var got []Load
	s.Record = func(l Load) { got = append(got, l) }
	// A burst in the first 30 s, then idle: the record averages 10 core-s
	// over the whole minute, not the last interval.
	for i, busy := range []uint64{0, 1000, 1000, 1000} {
		f.busy(busy)
		if err := s.sample(t0.Add(time.Duration(i) * 30 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1 in 90 s", len(got))
	}
	near(t, "minute cores", got[0].Groups[0].Cores, 10.0/60)
	if !got[0].At.Equal(t0.Add(60 * time.Second)) {
		t.Fatalf("record at %v", got[0].At)
	}
}

func TestRecordKeepsAScopeThatEndedInsideTheMinute(t *testing.T) {
	f := newTree(t, true)
	f.busy(0)
	scope := "dyna.slice/dyna-ses_a-aaaaaaaa.scope"
	s := f.sampler(4)
	var got []Load
	s.Record = func(l Load) { got = append(got, l) }
	step := func(at time.Duration, scopeUsec uint64, gone bool) {
		t.Helper()
		if gone {
			if err := os.RemoveAll(filepath.Join(f.cgrp, um, scope)); err != nil {
				t.Fatal(err)
			}
		} else {
			f.cpu(scope, scopeUsec)
			f.write(f.cgrp, filepath.Join(um, scope, "io.stat"), fmt.Sprintf("8:0 rbytes=%d wbytes=0\n", scopeUsec/1000*3))
		}
		if err := s.sample(t0.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	step(0, 0, false)
	step(30*time.Second, 20_000_000, false) // 20 core-s and 60000 bytes seen at t=30
	step(40*time.Second, 0, true)           // the turn ended; --collect removed the scope
	step(60*time.Second, 0, true)
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	for _, g := range got[0].Groups {
		if g.Name == "session:ses_a" {
			near(t, "ended session cores", g.Cores, 20.0/60)
			near(t, "ended session io", g.IO, 60000.0/60)
			return
		}
	}
	t.Fatalf("the ended session is missing from %+v", got[0].Groups)
}
