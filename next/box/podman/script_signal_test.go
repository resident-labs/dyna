package podman

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// scriptSignal carries complete newline-terminated records across detached shell
// boundaries. Keeping both ends open prevents launch-order and EOF races.
type scriptSignal struct {
	path   string
	file   *os.File
	reader *bufio.Reader
}

func newScriptSignal(t *testing.T) *scriptSignal {
	t.Helper()
	path := filepath.Join(t.TempDir(), "signal")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return &scriptSignal{path: path, file: file, reader: bufio.NewReader(file)}
}

func (s *scriptSignal) read(t *testing.T) string {
	t.Helper()
	if err := s.file.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	line, err := s.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("script signal %s: %v", s.path, err)
	}
	return strings.TrimSuffix(line, "\n")
}

func (s *scriptSignal) write(t *testing.T, value string) {
	t.Helper()
	if _, err := s.file.WriteString(value + "\n"); err != nil {
		t.Fatal(err)
	}
}

func (s *scriptSignal) process(t *testing.T) (pid, group int) {
	t.Helper()
	fields := strings.Fields(s.read(t))
	if len(fields) != 2 {
		t.Fatalf("script process record: %q", fields)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		t.Fatalf("script PID %q: %v", fields[0], err)
	}
	group, err = strconv.Atoi(fields[1])
	if err != nil || group <= 0 {
		t.Fatalf("script process group %q: %v", fields[1], err)
	}
	return pid, group
}

// assertRemoteGroupExited checks kernel state after confirmed cancellation, not
// merely local SSH completion. A grace delay would hide a false confirmation.
func assertRemoteGroupExited(t *testing.T, group int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "/usr/bin/ps", "-eo", "pgid=,stat=").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("process state record: %q", line)
		}
		got, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatal(err)
		}
		if got == group && !strings.HasPrefix(fields[1], "Z") && !strings.HasPrefix(fields[1], "X") {
			t.Fatalf("confirmed cancellation left live group %d: %s", group, line)
		}
	}
}
