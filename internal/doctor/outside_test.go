package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeProc writes one process the way /proc shows it. The command and cgroup
// are what the doctor reads; ticks is utime, and the stat fields in between
// are filler.
func fakeProc(t *testing.T, root string, pid int, command, cgroup, cwd string, ticks, pages uint64) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (%s) S 1 1 1 0 -1 0 0 0 0 0 %d 0 0 0 20 0 1 0 100 0 0", pid, command, ticks)
	for name, body := range map[string]string{"stat": stat, "statm": fmt.Sprintf("100 %d 0", pages), "cgroup": "0::" + cgroup + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(dir, "cwd"))
	if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
		t.Fatal(err)
	}
}

// D-273: the alert must name the programs behind "outside Dyna" load, grouped
// by unit, and leave Dyna's own processes out.
func TestTopOutsideNamesBusyProgramsOutsideDyna(t *testing.T) {
	root := t.TempDir()
	swarm := "/user.slice/user@1000.service/app.slice/horizon-swarm-3.scope"
	inside := "/user.slice/user@1000.service/dyna.slice/dyna-ses_x-12345678.scope"
	write := func(java uint64) {
		fakeProc(t, root, 10, "java", swarm, "/srv/horizon", java, 1000)
		fakeProc(t, root, 11, "java", swarm, "/srv/horizon", java, 1000)
		fakeProc(t, root, 12, "claude", inside, "/w", 900, 5000)
		fakeProc(t, root, 13, "idle", "/user.slice/app.slice/idle.service", "/", 0, 10)
	}
	write(0)
	got := TopOutside(root, 2*time.Second, func(time.Duration) { write(200) }, 3)
	if len(got) != 1 {
		t.Fatalf("got %+v, want only the busy outside group", got)
	}
	p := got[0]
	// Each java process used 200 ticks in 2 s: 1 core each.
	if p.Command != "java" || p.Unit != "horizon-swarm-3.scope" || p.Count != 2 || p.Cores < 1.99 || p.Cores > 2.01 || p.Cwd != "/srv/horizon" {
		t.Fatalf("group = %+v", p)
	}
}
