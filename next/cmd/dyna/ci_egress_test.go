package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box"
)

// A running runner has this directory bind-mounted, so a restarted proxy must reuse
// it and replace the socket inside.
func TestARestartedProxyServesTheSameSlotDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "3")
	first, err := egressSocket(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.(*net.UnixListener).SetUnlinkOnClose(false) // a killed proxy leaves its socket behind
	first.Close()
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := egressSocket(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	after, err := os.Stat(dir)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("the slot directory changed: %v", err)
	}
	conn, err := net.Dial("unix", filepath.Join(dir, box.EgressSocket))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
