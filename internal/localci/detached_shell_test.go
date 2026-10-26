package localci

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

// nodeStubs writes the programs a node script needs, so the real scripts run in
// a shell without systemd, docker or a user manager. The stub systemd-run runs
// the command after "--" in a session of its own, as a unit would, and waits for it; loginctl reports lingering.
func nodeStubs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"docker":      "#!/bin/sh\nexit 0\n",
		"git":         "#!/bin/sh\nexit 0\n",
		"systemctl":   "#!/bin/sh\ncase \"$2\" in is-active) exit 1 ;; *) exit 0 ;; esac\n",
		"loginctl":    "#!/bin/sh\necho yes\n",
		"systemd-run": "#!/bin/sh\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = \"--\" ]; then shift; break; fi\n  shift\ndone\nexec setsid -w \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// startOnShell runs the real start script with the check's script as input.
func startOnShell(t *testing.T, stubs, detached, script string, env ...string) error {
	t.Helper()
	cmd := exec.Command("bash", "-o", "pipefail", "-c", detachedStartScript, "dyna-ci", detached, "unit.service", "", "", "")
	cmd.Stdin = strings.NewReader(script)
	// The host check kills the rest of its process group, so the script runs in a
	// group of its own and never reaches the test binary.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH")), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Logf("start output: %s", out.String())
		return err
	}
	return nil
}

// A host check cleans up the rest of its process group when its command ends.
// That cleanup must not kill the detached wrapper that writes the exit record,
// or the log writer, or a passing check becomes a lost one.
func TestHostCheckKeepsItsExitRecordAndLog(t *testing.T) {
	dir := t.TempDir()
	stubs := nodeStubs(t)
	if err := os.MkdirAll(filepath.Join(dir, "computer-smoke", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	check := nodeCheck{id: SuiteComputerSmoke, name: "computer-smoke", host: true, inner: "echo ran-the-command; exit 0"}
	script := nodeCheckScript(Node{Name: "local", Slots: 1}, dir, filepath.Join(dir, "cache.git"), dir, "run", strings.Repeat("a", 40), strings.Repeat("b", 40), check, time.Minute, 0, false, DefaultActImage, machines.DefaultContainerSocket, "")
	keepActive(t, dir)
	detached := detachedDir(dir, check.id)
	if err := startOnShell(t, stubs, detached, script); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(filepath.Join(detached, "exit.json"))
	if err != nil || !strings.Contains(string(record), `"exitCode":0`) {
		t.Fatalf("the exit record is lost: %q %v", record, err)
	}
	log, _ := os.ReadFile(filepath.Join(detached, "log"))
	for _, want := range []string{"ran-the-command", "DYNA_CI_CHECK_EXIT=0"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("log lacks %q: %q", want, log)
		}
	}
}

// A start whose connection dropped after it made the folder, and before it
// launched anything, leaves a folder with no check in it. The retry must launch
// the check, not take the folder for a running one.
func TestStartAfterAnInterruptedLaunchLaunchesTheCheck(t *testing.T) {
	dir := t.TempDir()
	stubs := nodeStubs(t)
	detached := filepath.Join(dir, "check", "detached")
	if err := os.MkdirAll(detached, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(detached, "run.sh"), []byte("half written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startOnShell(t, stubs, detached, "echo launched; exit 4\n"); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(filepath.Join(detached, "exit.json"))
	if err != nil || !strings.Contains(string(record), `"exitCode":4`) {
		t.Fatalf("the retry did not launch the check: %q %v", record, err)
	}
	// A repeated start of a check that already finished does nothing.
	if err := startOnShell(t, stubs, detached, "exit 9\n"); err != nil {
		t.Fatal(err)
	}
	record, _ = os.ReadFile(filepath.Join(detached, "exit.json"))
	if !strings.Contains(string(record), `"exitCode":4`) {
		t.Fatalf("a repeated start ran the check again: %q", record)
	}
}

// One noisy check cannot fill the node's disk: the log keeps the start of the
// output up to a limit and says it discarded the rest, and the exit record still
// arrives.
func TestNodeLogIsBounded(t *testing.T) {
	dir := t.TempDir()
	stubs := nodeStubs(t)
	detached := filepath.Join(dir, "check", "detached")
	if err := startOnShell(t, stubs, detached, "head -c 100000 /dev/zero | tr '\\0' x; echo; exit 0\n", "DYNA_CI_LOG_CAP=100"); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(detached, "log"))
	if len(log) > 300 || !strings.Contains(string(log), "discarded on the node") {
		t.Fatalf("log = %d bytes, want a short log that names the discarded output: %q", len(log), log)
	}
	if record, err := os.ReadFile(filepath.Join(detached, "exit.json")); err != nil || !strings.Contains(string(record), `"exitCode":0`) {
		t.Fatalf("exit record = %q %v", record, err)
	}
}

// A setup whose first attempt started the run's keeper but lost the answer is
// repeated by the retry, and the repeat must not wait for the keeper's own lock.
func TestSetupRepeatedAfterAKeeperStartedSucceeds(t *testing.T) {
	base := t.TempDir()
	runDir := filepath.Join(base, "runs", "r")
	active := filepath.Join(runDir, ".active")
	cache := filepath.Join(base, "cache.git")
	script := nodeSetupScript(base, cache, filepath.Join(runDir, "reports"), active, nil)
	t.Cleanup(func() {
		_ = exec.Command("bash", "-c", removeRunDir(base, active)).Run()
	})
	for attempt := 1; attempt <= 2; attempt++ {
		cmd := exec.Command("bash", "-o", "pipefail", "-c", script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup attempt %d: %v: %s", attempt, err, out)
		}
	}
}
