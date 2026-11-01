//go:build integration

package podman

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

// configuredSSH supplies only the test's SSH configuration; Host still starts and
// controls the real OpenSSH master through its usual Runner boundary.
type configuredSSH struct{ config string }

func (r configuredSSH) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	if args[0] == "ssh" {
		args = append([]string{"ssh", "-F", r.config}, args[1:]...)
	}
	return (ExecRunner{}).Run(ctx, in, args...)
}

func TestRealSSHForwardReconnect(t *testing.T) {
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		if os.Getenv("DYNA_REQUIRE_SSHD") == "1" {
			t.Fatal("sshd is not installed")
		}
		t.Skip("sshd is not installed")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		if os.Getenv("DYNA_REQUIRE_SSHD") == "1" {
			t.Fatal("ssh-keygen is not installed")
		}
		t.Skip("ssh-keygen is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "dyna-sshd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	key := filepath.Join(dir, "key")
	if out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v: %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(auth, pub, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "sshd_config")
	contents := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nAllowUsers %s\nLogLevel VERBOSE\n", port, key, filepath.Join(dir, "pid"), auth, u.Username)
	if err := os.WriteFile(config, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", config)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		var lines strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			lines.WriteString(line + "\n")
			if strings.Contains(line, "Server listening") {
				ready <- ""
				io.Copy(io.Discard, stderr)
				return
			}
		}
		ready <- lines.String()
	}()
	select {
	case msg := <-ready:
		if msg != "" {
			t.Fatalf("sshd did not start: %s", msg)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	sshConfig := filepath.Join(dir, "ssh_config")
	contents = fmt.Sprintf("Host test\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s\n IdentitiesOnly yes\n StrictHostKeyChecking no\n UserKnownHostsFile /dev/null\n LogLevel ERROR\n", port, u.Username, key)
	if err := os.WriteFile(sshConfig, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := New("test", dir, "host", configuredSSH{sshConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer h.control(context.Background(), "exit")
	enginePath := filepath.Join(dir, "engine.sock")
	engine, err := net.Listen("unix", enginePath)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	go func() {
		for {
			c, err := engine.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	f := box.Forward{HostPath: filepath.Join(dir, "remote.sock"), EnginePath: enginePath}
	if err := h.AddForward(ctx, f); err != nil {
		t.Fatal(err)
	}
	exchange := func() {
		t.Helper()
		c, err := net.DialTimeout("unix", f.HostPath, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		if _, err := c.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 5)
		if _, err := io.ReadFull(c, b); err != nil || string(b) != "hello" {
			t.Fatalf("forward: %q: %v", b, err)
		}
	}
	exchange()
	check := exec.CommandContext(ctx, "ssh", "-F", sshConfig, "-S", h.socket, "-O", "check", "test")
	out, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("master check: %v: %s", err, out)
	}
	s := string(out)
	start := strings.Index(s, "pid=")
	if start < 0 {
		t.Fatalf("master PID: %s", out)
	}
	end := strings.Index(s[start+4:], ")")
	if end < 0 {
		t.Fatalf("master PID: %s", out)
	}
	pid, err := strconv.Atoi(s[start+4 : start+4+end])
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := h.AddForward(ctx, f); err != nil {
		t.Fatal(err)
	}
	exchange()
	// The forward is on the master and the socket is gone, as when a try removed it and died before ssh could answer. The
	// master still answers ok without binding a socket, which is why a retry cancels first.
	if err := os.Remove(f.HostPath); err != nil {
		t.Fatal(err)
	}
	if err := h.control(ctx, "forward", "-R", f.HostPath+":"+f.EnginePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.HostPath); !os.IsNotExist(err) {
		t.Fatalf("the master bound a forward it already held: %v", err)
	}
	if err := h.bind(ctx, f); err != nil {
		t.Fatal(err)
	}
	exchange()
}
