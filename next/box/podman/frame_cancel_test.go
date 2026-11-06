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
	cmd := exec.Command("bash", "-c", cancellableFrameScript, "dyna-frame", token, "true")
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
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case <-done:
			return cmd, wr, done
		default:
		}
		if _, err := os.Stat(filepath.Join(dir, "in")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receive phase never entered")
		}
		time.Sleep(time.Millisecond)
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
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait(); _ = os.RemoveAll(dir) }()
	deadline := time.Now().Add(3 * time.Second)
	var catPID string
	for time.Now().Before(deadline) {
		children, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid))
		for _, pid := range strings.Fields(string(children)) {
			comm, _ := os.ReadFile("/proc/" + pid + "/comm")
			if strings.TrimSpace(string(comm)) == "cat" {
				catPID = pid
			}
		}
		if catPID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if catPID == "" {
		t.Fatal("wrapper never entered blocked output copy")
	}
	out, err := exec.Command("bash", "-c", cancelRemoteScript, "dyna-cancel", token).CombinedOutput()
	if err != nil {
		t.Fatalf("cancel: %v %s", err, out)
	}
	time.Sleep(100 * time.Millisecond)
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
