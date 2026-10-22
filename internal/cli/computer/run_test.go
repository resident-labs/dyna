package computer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/computers"
)

func computerRunFixture(t *testing.T) context.Context {
	t.Helper()
	rootComputerFixture(t)
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	socket := computers.ControlSocket("")
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	done := make(chan error, 1)
	go func() {
		done <- computers.Serve(ctx, listener, func(_ context.Context, r computers.Request) (any, error) {
			switch r.Op {
			case "register":
				return computers.Meta{SessionID: r.Session, WaylandDisplay: "run-wayland", Display: ":999"}, nil
			case "run.prepare":
				if r.Status != "Checking the foreground command" {
					return nil, fmt.Errorf("bad intent: %q", r.Status)
				}
				return computers.Meta{SessionID: r.Session, WaylandDisplay: "run-wayland", Display: ":999"}, nil
			default:
				return nil, fmt.Errorf("unexpected operation %s", r.Op)
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return ctx
}

func TestComputerRunStreamsEnvironmentCwdAndExitStatus(t *testing.T) {
	ctx := computerRunFixture(t)
	inputText := ""
	command := func(args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
		cmd := NewCommand()
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		out, errout := new(bytes.Buffer), new(bytes.Buffer)
		cmd.SetOut(out)
		cmd.SetErr(errout)
		cmd.SetIn(strings.NewReader(inputText))
		cmd.SetContext(ctx)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out, errout, err
	}
	cwd := t.TempDir()
	out, errout, err := command("run", "--status", "Checking the foreground command", "--cwd", cwd, "--", "sh", "-c", `printf '%s|%s|%s' "$WAYLAND_DISPLAY" "$DISPLAY" "$PWD"; printf stderr-proof >&2; exit 7`)
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit: %v", err)
	}
	if out.String() != "run-wayland|:999|"+cwd || errout.String() != "stderr-proof" {
		t.Fatalf("stdout %q; stderr %q", out.String(), errout.String())
	}
	out, _, err = command("run", "--tty", "--status", "Checking the foreground command", "--", "sh", "-c", "test -t 0 && test -t 1 && test -t 2 && printf tty-proof")
	if err != nil || !strings.Contains(out.String(), "tty-proof") {
		t.Fatalf("TTY: %q %v", out.String(), err)
	}

	out, _, err = command("run", "--tty", "--status", "Checking the foreground command", "--", "sh", "-c", "head -c 131072 /dev/zero")
	if err != nil || out.Len() != 131072 {
		t.Fatalf("TTY final output: %d bytes, %v", out.Len(), err)
	}
	_, _, err = command("run", "--tty", "--status", "Checking the foreground command", "--", "sh", "-c", "kill -INT $$")
	if !errors.As(err, &exit) || exit.ExitCode() != 130 {
		t.Fatalf("TTY signal exit: %v", err)
	}

	for _, data := range []string{"hello\n", "hello"} {
		inputText = data
		out, _, err = command("run", "--tty", "--status", "Checking the foreground command", "--", "cat")
		if err != nil || !strings.Contains(out.String(), "hello") {
			t.Fatalf("TTY input %q: output %q, %v", data, out.String(), err)
		}
	}
	inputText = ""

	// The first write must arrive while the child is still waiting for stdin.
	input, send := io.Pipe()
	defer input.Close()
	defer send.Close()
	first := make(chan struct{}, 1)
	stream := &runSignalWriter{first: first}
	cmd := NewCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetContext(ctx)
	cmd.SetIn(input)
	cmd.SetOut(stream)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "--status", "Checking the foreground command", "--", "sh", "-c", "printf ready; read value; printf finished"})
	ended := make(chan error, 1)
	go func() { ended <- cmd.Execute() }()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("stdout was not streamed before command exit")
	}
	if _, err := io.WriteString(send, "continue\n"); err != nil {
		t.Fatal(err)
	}
	send.Close()
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if stream.String() != "readyfinished" {
		t.Fatalf("stream: %q", stream.String())
	}
}

type runSignalWriter struct {
	buffer bytes.Buffer
	first  chan struct{}
}

func (w *runSignalWriter) String() string { return w.buffer.String() }

func (w *runSignalWriter) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	select {
	case w.first <- struct{}{}:
	default:
	}
	return n, err
}

func TestComputerRunRequiresIntentBeforeSetup(t *testing.T) {
	cmd := NewCommand()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"run", "--", "sh", "-c", "exit 0"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--status is required") {
		t.Fatalf("missing intent: %v", err)
	}
}
