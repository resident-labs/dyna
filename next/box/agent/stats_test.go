package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A fake cgroup, a clock moved by hand and a du that counts its runs.
type statsFixture struct {
	stats *boxStats
	clock atomic.Int64 // read by the du goroutine too
	runs  atomic.Int32
}

func (f *statsFixture) now() time.Time { return time.Unix(0, f.clock.Load()) }

func (f *statsFixture) wait(d time.Duration) { f.clock.Add(int64(d)) }

func newStatsFixture(t *testing.T) *statsFixture {
	t.Helper()
	f := &statsFixture{}
	f.clock.Store(time.Unix(1000, 0).UnixNano())
	f.stats = &boxStats{root: t.TempDir(), now: f.now, du: func(context.Context) (int64, error) {
		f.runs.Add(1)
		return 12 << 30, nil
	}}
	f.window(t, 500_000)
	f.write(t, "memory.current", "1000000\n")
	f.write(t, "memory.stat", "anon 500000\nfile 400000\ninactive_file 300000\n")
	f.cpu(t, 375_000)
	return f
}

// window makes the CPU window end at once, with the counter at usec.
func (f *statsFixture) window(t *testing.T, usec int) {
	t.Helper()
	f.stats.after = func(wait time.Duration) <-chan time.Time {
		f.wait(wait)
		f.cpu(t, usec)
		done := make(chan time.Time, 1)
		done <- f.now()
		return done
	}
}

func (f *statsFixture) write(t *testing.T, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.stats.root, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *statsFixture) cpu(t *testing.T, usec int) {
	t.Helper()
	f.write(t, "cpu.stat", fmt.Sprintf("usage_usec %d\nuser_usec 1\nsystem_usec 2\n", usec))
}

func (f *statsFixture) waitRuns(t *testing.T, want int32) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		f.stats.mu.Lock()
		idle := !f.stats.measuring
		f.stats.mu.Unlock()
		if idle && f.runs.Load() == want {
			return
		}
	}
	t.Fatalf("du ran %d times, want %d", f.runs.Load(), want)
}

func TestReadingCountsMemoryLessReclaimableCache(t *testing.T) {
	f := newStatsFixture(t)
	reading, err := f.stats.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reading.MemUsed != 700_000 {
		t.Fatalf("memory %d", reading.MemUsed)
	}
}

func TestFirstCPUReadingMeasuresAWindowAndLaterOnesTheGap(t *testing.T) {
	f := newStatsFixture(t)
	first, err := f.stats.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUUsed != 0.5 {
		t.Fatalf("first reading %v", first.CPUUsed)
	}
	f.wait(5 * time.Second)
	f.cpu(t, 500_000+10_000_000) // two cores for five seconds
	second, err := f.stats.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.CPUUsed != 2 {
		t.Fatalf("second reading %v", second.CPUUsed)
	}
}

func TestReadingsInTheSameMomentShareACPUFigure(t *testing.T) {
	f := newStatsFixture(t)
	first, err := f.stats.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.wait(time.Millisecond)
	second, err := f.stats.read(context.Background())
	if err != nil || second.CPUUsed != first.CPUUsed {
		t.Fatalf("second reading %v, %v", second.CPUUsed, err)
	}
}

func TestStaleCPUSampleStartsAFreshWindow(t *testing.T) {
	f := newStatsFixture(t)
	if _, err := f.stats.read(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.wait(time.Hour)
	f.cpu(t, 90_000_000) // a counter that moved long ago says nothing about now
	f.window(t, 90_000_000+250_000)
	reading, err := f.stats.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reading.CPUUsed != 1 {
		t.Fatalf("reading %v", reading.CPUUsed)
	}
}

func TestDiskIsMeasuredOnceInItsWindowAndNeverWaitedFor(t *testing.T) {
	f := newStatsFixture(t)
	release := make(chan struct{})
	f.stats.du = func(context.Context) (int64, error) {
		f.runs.Add(1)
		<-release
		return 12 << 30, nil
	}
	for range 2 {
		reading, err := f.stats.read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if reading.DiskUsed != 0 {
			t.Fatalf("a du still running gave %d", reading.DiskUsed)
		}
	}
	close(release)
	f.waitRuns(t, 1)
	reading, err := f.stats.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reading.DiskUsed != 12<<30 {
		t.Fatalf("disk %d", reading.DiskUsed)
	}
	f.waitRuns(t, 1)
	f.wait(diskEvery)
	if _, err := f.stats.read(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.waitRuns(t, 2)
}

func TestFailedDuKeepsTheLastFigureAndWaitsItsTurn(t *testing.T) {
	f := newStatsFixture(t)
	if _, err := f.stats.read(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.waitRuns(t, 1)
	f.stats.du = func(context.Context) (int64, error) {
		f.runs.Add(1)
		return 0, errors.New("no du here")
	}
	f.wait(diskEvery)
	if _, err := f.stats.read(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.waitRuns(t, 2)
	reading, err := f.stats.read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.waitRuns(t, 2)
	if reading.DiskUsed != 12<<30 {
		t.Fatalf("disk %d", reading.DiskUsed)
	}
}

func TestReadingFailsWithoutACgroup(t *testing.T) {
	f := newStatsFixture(t)
	f.stats.root = filepath.Join(f.stats.root, "gone")
	if _, err := f.stats.read(context.Background()); err == nil {
		t.Fatal("no error")
	}
}

func TestDuBytes(t *testing.T) {
	if got, err := duBytes("2048\t/box\n", nil); err != nil || got != 2048*1024 {
		t.Fatalf("%d, %v", got, err)
	}
	if _, err := duBytes("", errors.New("nice: du: not found")); err == nil {
		t.Fatal("no error for a du that never ran")
	}
}

func TestMountsUnder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mountinfo")
	lines := "1 2 0:1 / / rw - overlay overlay rw\n" +
		"3 1 8:1 /a /box rw - ext4 /dev/x rw\n" +
		"4 3 8:1 /b /box/cache rw - ext4 /dev/x rw\n" +
		"5 3 8:1 /c /box/shared rw - ext4 /dev/x rw\n" +
		"6 1 8:1 /d /boxes rw - ext4 /dev/x rw\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	got := mountsUnder(path, "/box")
	if len(got) != 2 || got[0] != "/box/cache" || got[1] != "/box/shared" {
		t.Fatalf("%v", got)
	}
}
