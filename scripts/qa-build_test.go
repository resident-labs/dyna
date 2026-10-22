package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

type qaBuildWorker struct {
	input io.WriteCloser
	lines chan string
	done  chan struct{}
	err   error
	cmd   *exec.Cmd
}

func (w *qaBuildWorker) line(t *testing.T) string {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case line := <-w.lines:
		return line
	case <-w.done:
		t.Fatalf("QA launcher ended before reporting its phase: %v", w.err)
	case <-timer.C:
		t.Fatal("QA launcher did not report its next phase")
	}
	return ""
}

func (w *qaBuildWorker) send(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(w.input, "continue\n"); err != nil {
		t.Fatal(err)
	}
}

func (w *qaBuildWorker) wait(t *testing.T) error {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-w.done:
		return w.err
	case <-timer.C:
		t.Fatal("QA launcher did not exit")
		return nil
	}
}

func qaBuildFixture(t *testing.T) (func(string) *qaBuildWorker, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "stopped")
	scripts := map[string]string{
		"npm":  "#!/bin/sh\n[ \"$GOMAXPROCS\" = 4 ]\n",
		"nice": "#!/bin/sh\n[ \"$1 $2\" = '-n 10' ] || exit 72\nshift 2\nexec \"$@\"\n",
		"go": `#!/bin/sh
[ "$GOMAXPROCS" = 4 ] || exit 72
out=
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then shift; out=$1; fi
  shift
done
printf 'build-started\n'
read -r _
printf '#!/bin/sh\nprintf "qa-started\\n"\nread -r _\n' > "$out"
chmod +x "$out"
`,
		"dyna": "#!/bin/sh\n[ \"$*\" = 'browser stop' ] || exit 72\nprintf '%s\\n' \"$QA_TEST_NAME\" >> \"$QA_STOP_LOG\"\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	launcher, err := filepath.Abs("qa-branch.sh")
	if err != nil {
		t.Fatal(err)
	}
	var workers []*qaBuildWorker
	t.Cleanup(func() {
		for _, w := range workers {
			_ = w.cmd.Process.Signal(syscall.SIGTERM)
			_ = w.input.Close()
			select {
			case <-w.done:
			case <-time.After(30 * time.Second):
				_ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
				t.Error("QA launcher cleanup did not finish")
			}
		}
	})
	start := func(name string) *qaBuildWorker {
		cmd := exec.Command("bash", launcher, "--web", "web/dist", "--status", "Test QA builds")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "DYNA_SCRATCH="+dir,
			"DYNA_BIN="+filepath.Join(dir, "dyna"), "XDG_RUNTIME_DIR="+dir,
			"QA_TEST_NAME="+name, "QA_STOP_LOG="+log)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		w := &qaBuildWorker{input: input, lines: make(chan string, 16), done: make(chan struct{}), cmd: cmd}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		workers = append(workers, w)
		go func() {
			scanner := bufio.NewScanner(output)
			for scanner.Scan() {
				w.lines <- scanner.Text()
			}
			w.err = cmd.Wait()
			close(w.done)
		}()
		return w
	}
	return start, log
}

func TestQABuildSlotsBoundBuildsAndStopBrowsers(t *testing.T) {
	start, stopped := qaBuildFixture(t)
	first := start("first")
	if line := first.line(t); line != "build-started" {
		t.Fatalf("first build did not enter: %q", line)
	}
	second := start("second")
	if line := second.line(t); line != "build-started" {
		t.Fatalf("second build did not enter: %q", line)
	}
	third := start("third")
	if line := third.line(t); line != "waiting for a QA build slot" {
		t.Fatalf("third build bypassed the two-slot queue: %q", line)
	}
	first.send(t)
	if line := first.line(t); line != "qa-started" {
		t.Fatalf("first QA did not start: %q", line)
	}
	if line := third.line(t); line != "build-started" {
		t.Fatalf("finished build kept its slot during QA: %q", line)
	}
	second.send(t)
	third.send(t)
	for _, w := range []*qaBuildWorker{second, third} {
		if line := w.line(t); line != "qa-started" {
			t.Fatalf("QA did not start: %q", line)
		}
	}
	for _, w := range []*qaBuildWorker{first, second, third} {
		w.send(t)
		if err := w.wait(t); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(stopped)
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Fields(string(data))
	sort.Strings(names)
	if fmt.Sprint(names) != "[first second third]" {
		t.Fatalf("QA browsers were not stopped: %s", data)
	}
}

func TestQACanceledBuildReleasesSlot(t *testing.T) {
	start, stopped := qaBuildFixture(t)
	first := start("first")
	if line := first.line(t); line != "build-started" {
		t.Fatalf("first build did not start: %q", line)
	}
	second := start("second")
	if line := second.line(t); line != "build-started" {
		t.Fatalf("second build did not start: %q", line)
	}
	third := start("third")
	if line := third.line(t); line != "waiting for a QA build slot" {
		t.Fatalf("third build bypassed admission: %q", line)
	}
	if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := first.wait(t); !errors.As(err, &exit) || exit.ExitCode() != 143 {
		t.Fatalf("canceled build status changed: %v", err)
	}
	if line := third.line(t); line != "build-started" {
		t.Fatalf("canceled build retained its slot: %q", line)
	}
	for _, w := range []*qaBuildWorker{second, third} {
		w.send(t)
		if line := w.line(t); line != "qa-started" {
			t.Fatalf("QA did not start: %q", line)
		}
		w.send(t)
		if err := w.wait(t); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(stopped)
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Fields(string(data))
	sort.Strings(names)
	if fmt.Sprint(names) != "[second third]" {
		t.Fatalf("browser cleanup included an unstarted QA or missed a browser: %s", data)
	}
}
