package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProc writes a /proc with an IO stall total and one write_bytes per process.
func fakeProc(t *testing.T, stallMicros int, writes map[int]int) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pressure"), 0o755); err != nil {
		t.Fatal(err)
	}
	pressure := fmt.Sprintf("some avg10=0.00 avg60=0.00 avg300=0.00 total=%d\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1\n", stallMicros)
	if err := os.WriteFile(filepath.Join(root, "pressure", "io"), []byte(pressure), 0o644); err != nil {
		t.Fatal(err)
	}
	for pid, bytes := range writes {
		dir := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"io":   fmt.Sprintf("rchar: 1\nwrite_bytes: %d\n", bytes),
			"comm": fmt.Sprintf("proc%d\n", pid),
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := procRoot
	procRoot, lastIO = root, nil
	t.Cleanup(func() { procRoot, lastIO = old, nil })
}

// A slow checkpoint names the processes that wrote most since the last slow
// one and the IO stall time in between. A writer that exited before the reading
// (pid 50 here) cannot be ranked, so the line counts it. The first slow
// checkpoint has no earlier reading.
func TestIOBlameNamesTopWritersSinceLastSlowCheckpoint(t *testing.T) {
	fakeProc(t, 1_000_000, map[int]int{10: 1 << 20, 20: 5 << 20, 30: 2 << 20, 50: 3 << 20})
	if got := ioBlame(); !strings.Contains(got, "no earlier reading") {
		t.Fatalf("first reading = %q", got)
	}
	fakeProcKeepingLast(t, 4_500_000, map[int]int{10: 1<<20 + 400<<10, 20: 5<<20 + 9<<20, 30: 2<<20 + 100<<10, 40: 1 << 30})
	got := ioBlame()
	want := "io: stalled 3500 ms since the last slow checkpoint; top visible writers: proc40[40] 1048576 KB proc20[20] 9216 KB proc10[10] 400 KB; 1 earlier processes exited and are not ranked"
	if got != want {
		t.Fatalf("ioBlame = %q, want %q", got, want)
	}
}

// fakeProcKeepingLast swaps in a new fake /proc and keeps the previous reading.
func fakeProcKeepingLast(t *testing.T, stallMicros int, writes map[int]int) {
	t.Helper()
	prev := lastIO
	fakeProc(t, stallMicros, writes)
	lastIO = prev
}
