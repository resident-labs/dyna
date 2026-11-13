package podman

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func remoteReceiveFrame(t *testing.T, token string) (*exec.Cmd, io.WriteCloser, <-chan error) {
	t.Helper()
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ready := newScriptSignal(t)
	needle := "head -c \"$size\" > \"$dir/in\""
	if strings.Count(cancellableFrameScript, needle) != 1 {
		t.Fatal("frame receive anchor changed")
	}
	script := strings.Replace(cancellableFrameScript, needle, fmt.Sprintf("(echo ready > %q; exec head -c \"$size\") > \"$dir/in\"", ready.path), 1)
	cmd := exec.Command("bash", "-c", script, "dyna-frame", token, "true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = rd
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rd.Close()
	done := make(chan error, 1)
	go func() { defer close(done); done <- cmd.Wait() }()
	dir := filepath.Join("/tmp", "dyna-start-"+token)
	t.Cleanup(func() { wr.Close(); _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = os.RemoveAll(dir) })
	if _, err := fmt.Fprintf(wr, "DYNA-IN %020d\nx", 1<<30); err != nil {
		t.Fatal(err)
	}
	announced := make(chan error, 1)
	go func() {
		line, err := ready.reader.ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("frame readiness: %q", line)
		}
		announced <- err
	}()
	select {
	case err := <-announced:
		if err != nil {
			t.Fatal(err)
		}
	case <-done:
		_ = ready.file.Close()
		<-announced
	case <-time.After(3 * time.Second):
		_ = ready.file.Close()
		<-announced
		t.Fatal("receive phase never entered")
	}
	return cmd, wr, done
}

func TestCancellationDuringFrameReceiveIsConfirmedOnlyAfterRemoteExit(t *testing.T) {
	token := fmt.Sprintf("remote-receive-%d", time.Now().UnixNano())
	cmd, wr, done := remoteReceiveFrame(t, token)
	out, err := exec.Command("bash", "-c", cancelRemoteScript, "dyna-cancel", token).CombinedOutput()
	if err != nil {
		t.Fatalf("cancel: %v %s", err, out)
	}
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		children, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid))
		t.Errorf("cancel reported success but wrapper pid %d and receive worker(s) %s are alive, with no recorded child group", cmd.Process.Pid, strings.TrimSpace(string(children)))
	}
	wr.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("receive wrapper did not exit even after transport EOF")
	}
}

func remoteLivePID(t *testing.T, pid string) bool {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + pid + "/stat")
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	end := strings.LastIndex(string(raw), ") ")
	if end < 0 {
		t.Fatalf("invalid proc stat for %s: %s", pid, raw)
	}
	fields := strings.Fields(string(raw[end+2:]))
	if len(fields) == 0 {
		t.Fatalf("missing proc state for %s", pid)
	}
	return fields[0] != "Z" && fields[0] != "X"
}

func TestCancellationAlsoJoinsRemoteOutputWorkers(t *testing.T) {
	token := fmt.Sprintf("remote-output-%d", time.Now().UnixNano())
	dir := filepath.Join("/tmp", "dyna-start-"+token)
	ready := newScriptSignal(t)
	tools := t.TempDir()
	cat := fmt.Sprintf("#!/bin/bash\nif [[ \"$1\" = %q ]]; then printf '%%s %%s\\n' \"$$\" \"$(/usr/bin/ps -o pgid= -p \"$$\")\" > %q; fi\nexec /usr/bin/cat \"$@\"\n", filepath.Join(dir, "out"), ready.path)
	if err := os.WriteFile(filepath.Join(tools, "cat"), []byte(cat), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()
	cmd := exec.Command("bash", "-c", cancellableFrameScript, "dyna-frame", token, "head", "-c", "1048576", "/dev/zero")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(fmt.Sprintf("DYNA-IN %020d\n", 0))
	cmd.Stdout = wr
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The test is this leader's parent. Reap it during confirmation, just as
	// sshd would, rather than retaining our own zombie until cancel returns.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); <-done; _ = os.RemoveAll(dir) }()
	pid, group := ready.process(t)
	catPID := fmt.Sprint(pid)
	if err := rd.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(rd, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	// Reading one byte proves cat is writing; the unread megabyte cannot fit in the pipe.
	out, err := exec.Command("bash", "-c", cancelRemoteScript, "dyna-cancel", token).CombinedOutput()
	if err != nil {
		t.Fatalf("cancel: %v %s", err, out)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not join output wrapper")
	}
	assertRemoteGroupExited(t, group)
	wrapperLive := remoteLivePID(t, fmt.Sprint(cmd.Process.Pid))
	catLive := remoteLivePID(t, catPID)
	if wrapperLive || catLive {
		t.Errorf("cancel reported success but live work remains: wrapper pid %d live=%v; output cat pid %s live=%v", cmd.Process.Pid, wrapperLive, catPID, catLive)
	}
}

func TestCancellationMarkerFencesFrameReceiveBeforeLateLaunch(t *testing.T) {
	token := fmt.Sprintf("remote-late-receive-%d", time.Now().UnixNano())
	out, err := exec.Command("bash", "-c", cancelRemoteScript, "dyna-cancel", token).CombinedOutput()
	if err != nil {
		t.Fatalf("cancel: %v %s", err, out)
	}
	cmd, wr, done := remoteReceiveFrame(t, token)
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Errorf("preexisting cancellation marker did not fence late wrapper %d: it started buffering a 1 GiB frame", cmd.Process.Pid)
	}
	wr.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("receive wrapper did not exit after EOF")
	}
}
