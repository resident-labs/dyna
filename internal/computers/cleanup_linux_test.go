//go:build linux

package computers

import (
	"context"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/store"
)

func TestForkSetsidCleanupHelper(t *testing.T) {
	if os.Getenv("DYNA_COMPUTER_CLEANUP_HELPER") != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	id := testSessionID(t, "cleanup-helper")
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	env := mergeEnvironment(os.Environ(), map[string]string{"DYNA_COMPUTER_PROCESS": id})
	p, err := startProcess(env, filepath.Join(Dir(id), "child.log"), "/bin/sh", "-c", "setsid /bin/sh -c 'sleep 600 & wait' & wait")
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	s := &supervisor{ctx: context.Background(), meta: Meta{SessionID: id}, compositor: p, apps: map[int]string{}, relayConns: map[*net.UnixConn]bool{}, viewers: map[string]string{}, state: Status{SessionID: id}}
	var owned map[int]string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		owned = s.owned()
		if len(owned) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(owned) < 3 {
		t.Fatalf("fork/setsid descendants not created: %+v", owned)
	}
	s.stop()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reapAdopted(owned)
		remaining := 0
		all := processes()
		for pid, start := range owned {
			if all[pid].start == start {
				remaining++
			}
		}
		if remaining == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("processes survived cleanup: %+v", owned)
}
func TestDestroyOfflineKeepsHistory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	id := "offline"
	os.MkdirAll(Dir(id), 0700)
	m := Meta{SessionID: id, WaylandDisplay: publicName(id), Display: ":999"}
	store.WriteJSON(filepath.Join(Dir(id), "meta.json"), m, 0o600)
	os.MkdirAll(filepath.Join(Dir(id), "profiles"), 0700)
	os.WriteFile(filepath.Join(Dir(id), "last.png"), []byte("history"), 0600)
	if err := Destroy(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if savedStatus(id).State != StateDestroyed {
		t.Fatal("record was not destroyed")
	}
	if _, err := os.Stat(filepath.Join(Dir(id), "profiles")); !os.IsNotExist(err) {
		t.Fatal("profiles remain")
	}
	if b, _ := os.ReadFile(filepath.Join(Dir(id), "last.png")); string(b) != "history" {
		t.Fatal("history removed")
	}
}
