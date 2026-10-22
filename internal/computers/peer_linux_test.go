//go:build linux

package computers

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPeerUIDHelper(t *testing.T) {
	if address := os.Getenv("DYNA_COMPUTER_PEER_HELPER"); address != "" {
		conn, err := net.Dial("unix", address)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
}
func TestAbstractSocketRejectsOtherUnixUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing client UID needs root; the container CI runs this test")
	}
	dir, err := os.MkdirTemp("/tmp", "dcp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	helper := filepath.Join(dir, "peer.test")
	dst, err := os.OpenFile(helper, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(dst, src)
	dst.Close()
	if err != nil {
		t.Fatal(err)
	}
	address := "@dyna-computer-peer-" + filepath.Base(dir)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	listener.SetDeadline(time.Now().Add(5 * time.Second))
	cmd := exec.Command(helper, "-test.run=^TestPeerUIDHelper$")
	cmd.Dir = "/tmp"
	cmd.Env = mergeEnvironment(os.Environ(), map[string]string{"DYNA_COMPUTER_PEER_HELPER": address})
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if pid, err := peerPID(conn); err == nil {
		t.Fatalf("accepted client PID %d from another Unix user", pid)
	}
}
