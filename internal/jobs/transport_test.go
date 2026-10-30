package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/testsafe"
)

func TestTransportRetriesTruncatedTrailer(t *testing.T) {
	var out, diagnostic bytes.Buffer
	calls := 0
	x := Executor{Node: Conn{Name: "node", SSH: "node"}, Wait: func(context.Context, time.Duration) error { return nil }}
	x.Command = func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		calls++
		if calls == 1 {
			fmt.Fprint(out, "cutDYNA-END 00000000000000000005 000\n")
		} else {
			fmt.Fprint(out, "wholeDYNA-END 00000000000000000005 000\n")
		}
		return nil
	}
	if err := x.NodeCommand(t.Context(), &out, &diagnostic, "true"); err != nil || calls != 2 || out.String() != "whole" {
		t.Fatalf("out = %q, calls = %d, err = %v", out.String(), calls, err)
	}
}

func TestTransportFullTrailerCarriesStatus(t *testing.T) {
	x := Executor{Node: Conn{Name: "node", SSH: "node"}}
	x.Command = func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, _ ...string) error {
		fmt.Fprint(out, "resultDYNA-END 00000000000000000006 255\n")
		return nil
	}
	var out bytes.Buffer
	err := x.NodeExec(t.Context(), nil, &out, io.Discard, "true")
	code, ok := CommandExitStatus(err)
	if !ok || code != 255 || x.transient(err) || out.String() != "result" {
		t.Fatalf("out = %q, status = %d, ok = %v, err = %v", out.String(), code, ok, err)
	}
}

func TestTransportSSHUsesPrivateMaster(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	x := Executor{Node: Conn{Name: "node", SSH: "node"}}
	_, argv := x.NodeProgram("true", nil)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "ControlMaster=auto") || !strings.Contains(joined, "ControlPersist=10m") || !strings.Contains(joined, "ControlPath=") || strings.Contains(joined, "ControlPath=none") {
		t.Fatalf("ssh options = %q", joined)
	}
}

func TestTransportFallsBackWhenRuntimeUnavailable(t *testing.T) {
	runtime := filepath.Join(testsafe.ShortTempDir(t), "not-a-directory")
	if err := os.WriteFile(runtime, nil, 0600); err != nil {
		t.Fatal(err)
	}
	dir := testsafe.ShortTempDir(t)
	fallback := filepath.Join(dir, "private")
	x := Executor{Node: Conn{Name: "node", SSH: "node"}}
	joined := strings.Join(x.sshOptionsIn(runtime, fallback), " ")
	if !strings.Contains(joined, "ControlMaster=auto") || !strings.Contains(joined, "ControlPath="+fallback+"/") {
		t.Fatalf("ssh options = %q, want private fallback", joined)
	}
	info, err := os.Stat(fallback)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("fallback mode: %v, %v", info, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(fallback, link); err != nil {
		t.Fatal(err)
	}
	joined = strings.Join(x.sshOptionsIn(runtime, link), " ")
	if !strings.Contains(joined, "ControlMaster=no") || !strings.Contains(joined, "ControlPath=none") {
		t.Fatalf("ssh options = %q, want no sharing for a symlink", joined)
	}
}

func TestRemoteFrameStreamsBodyAndStatus(t *testing.T) {
	cmd := exec.Command("bash", "-o", "pipefail", "-c", remoteFrame, "dyna-frame", `cat; exit 7`)
	cmd.Stdin = strings.NewReader("DYNA-IN 00000000000000000005\nhello")
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader := os.NewFile(uintptr(fds[0]), "reply-reader")
	writer := os.NewFile(uintptr(fds[1]), "reply-writer")
	defer reader.Close()
	cmd.Stdout = writer
	output := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(reader)
		output <- string(body)
	}()
	err = cmd.Run()
	writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := <-output; got != "helloDYNA-END 00000000000000000005 007\n" {
		t.Fatalf("reply = %q", got)
	}
}

func TestRemoteInputMustBeCompleteBeforeExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	cmd := exec.Command("bash", "-o", "pipefail", "-c", remoteFrame, "dyna-frame", "touch "+marker)
	cmd.Stdin = strings.NewReader("DYNA-IN 00000000000000000005\nabc")
	if err := cmd.Run(); err == nil {
		t.Fatal("short input succeeded")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("short input ran the command: %v", err)
	}
}
