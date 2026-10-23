//go:build linux

package computers

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/store"
	"golang.org/x/sys/unix"
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
	ready := readyFIFO(t)
	env := mergeEnvironment(os.Environ(), map[string]string{"DYNA_COMPUTER_PROCESS": id, "CLEANUP_READY": ready})
	// The detached shell signals from inside its own session, after it starts
	// its sleep helper, so the FIFO write proves the setsid descendant and its
	// helper exist rather than only the fork that created them.
	script := `setsid /bin/sh -c 'sleep 600 & printf ready > "$CLEANUP_READY"; wait' & wait`
	p, err := startProcess(env, filepath.Join(Dir(id), "child.log"), "/bin/sh", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop()
	s := &supervisor{ctx: context.Background(), meta: Meta{SessionID: id}, compositor: p, apps: map[int]string{}, relayConns: map[*net.UnixConn]bool{}, viewers: map[string]string{}, state: Status{SessionID: id}}
	awaitReady(t, ready)
	owned := s.owned()
	if len(owned) < 3 {
		t.Fatalf("fork/setsid descendants not created: %+v", owned)
	}
	if !ownsDetachedSession(owned) {
		t.Fatalf("no detached descendant session to clean up: %+v", owned)
	}
	s.stop()
	for pid := range owned {
		waitProcessGone(t, pid, "processes survived cleanup")
	}
	reapAdopted(owned)
	all := processes()
	for pid, start := range owned {
		if all[pid].start == start {
			t.Fatalf("processes survived cleanup: %+v", owned)
		}
	}
}

// ownsDetachedSession reports whether an owned process leads its own session,
// so the cleanup set contains a setsid descendant and not only its parent.
func ownsDetachedSession(owned map[int]string) bool {
	for pid := range owned {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			continue
		}
		line := string(b)
		end := strings.LastIndexByte(line, ')')
		if end < 0 {
			continue
		}
		// After the command name: state, ppid, pgrp, session.
		fields := strings.Fields(line[end+1:])
		if len(fields) >= 4 && fields[3] == strconv.Itoa(pid) {
			return true
		}
	}
	return false
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
