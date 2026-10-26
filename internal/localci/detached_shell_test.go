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
	// The start script asks the login shell for a PATH. This one answers with the
	// PATH it was given, so the host's own profiles never change the stubs.
	shell := filepath.Join(stubs, "login-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nprintf '%s\\n' \"$PATH\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"), "SHELL="+shell), env...)
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
	if err := startOnShell(t, stubs, detached, "head -c 100000 /dev/zero | tr '\\0' x; echo; echo DYNA_CI_CHECK_EXIT=0 >> \"$DYNA_CI_TRAILER\"; exit 0\n", "DYNA_CI_LOG_CAP=100"); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(detached, "log"))
	if len(log) > 300 || !strings.Contains(string(log), "discarded on the node") {
		t.Fatalf("log = %d bytes, want a short log that names the discarded output: %q", len(log), log)
	}
	// The completion marker is the end of the log, whatever the output did.
	if !strings.HasSuffix(string(log), "DYNA_CI_CHECK_EXIT=0\n") {
		t.Fatalf("the log does not end with the completion marker: %q", log)
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

// A daemon that keeps the check's output open cannot hold the wrapper, and once
// the wrapper has ended the log writer is gone with it, so nothing lands after
// the completion marker.
func TestWrapperEndsTheLogWriterWithItsGroup(t *testing.T) {
	dir := t.TempDir()
	stubs := nodeStubs(t)
	detached := filepath.Join(dir, "check", "detached")
	daemon := filepath.Join(dir, "daemon.pid")
	script := "sleep 30 & echo $! > " + daemon + "\necho DYNA_CI_CHECK_EXIT=0 >> \"$DYNA_CI_TRAILER\"\nexit 0\n"
	t.Cleanup(func() {
		if pid, err := os.ReadFile(daemon); err == nil {
			_ = exec.Command("kill", strings.TrimSpace(string(pid))).Run()
		}
	})
	if err := startOnShell(t, stubs, detached, script, "DYNA_CI_LOG_CAP=77777", "DYNA_CI_DRAIN_TENTHS=1"); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(detached, "log"))
	if !strings.HasSuffix(string(log), "DYNA_CI_CHECK_EXIT=0\n") {
		t.Fatalf("the log does not end with the completion marker: %q", log)
	}
	if out, _ := exec.Command("pgrep", "-f", "head -c 77777").Output(); len(out) != 0 {
		ps, _ := exec.Command("ps", "-o", "pid,ppid,pgid,sid,args", "-p", strings.TrimSpace(string(out))).CombinedOutput()
		t.Fatalf("the log writer survived the wrapper: %s %s", out, ps)
	}
}

// teardownStubs writes a docker and a systemctl whose answers a test chooses:
// DOCKER_PS_EXIT and DOCKER_PS_OUT for the container queries, DOCKER_RM_EXIT for
// removals, SYSTEMCTL_SHOW_EXIT and UNIT_STATE for the unit queries.
func teardownStubs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"docker":    "#!/bin/sh\ncase \"$1\" in info) exit 0 ;; rm) exit ${DOCKER_RM_EXIT:-0} ;; ps) printf '%s' \"$DOCKER_PS_OUT\"; exit ${DOCKER_PS_EXIT:-0} ;; esac\nexit 0\n",
		"systemctl": "#!/bin/sh\ncase \"$2\" in show) [ \"${SYSTEMCTL_SHOW_EXIT:-0}\" = 0 ] || exit \"$SYSTEMCTL_SHOW_EXIT\"; echo \"${UNIT_STATE:-inactive}\"; exit 0 ;; *) exit 0 ;; esac\n",
		"git":       "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A status query that fails is not proof that nothing runs. The teardown of an
// old run passes only when each query answers and finds nothing left, so a
// Docker or user-manager outage keeps the run's workspace and record.
func TestTeardownNeedsAnswersThatNothingSurvives(t *testing.T) {
	image := nodeCheck{id: "unit", image: "go:1"}
	host := nodeCheck{id: "host", host: true}
	for _, tc := range []struct {
		name   string
		check  nodeCheck
		env    []string
		wantOK bool
	}{
		{"everything stopped", image, nil, true},
		{"docker queries fail", image, []string{"DOCKER_RM_EXIT=1", "DOCKER_PS_EXIT=1"}, false},
		{"container still running", image, []string{"DOCKER_PS_OUT=dyna-ci-r1-unit\n"}, false},
		{"another container is no survivor", image, []string{"DOCKER_PS_OUT=dyna-ci-r1-other\n"}, true},
		{"host scope stopped", host, nil, true},
		{"user manager does not answer", host, []string{"SYSTEMCTL_SHOW_EXIT=1"}, false},
		{"host scope still active", host, []string{"UNIT_STATE=active"}, false},
		{"unit query fails for an image check", image, []string{"SYSTEMCTL_SHOW_EXIT=1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := teardownScript(Node{Name: "local"}, "/cache", t.TempDir(), "r1", []nodeCheck{tc.check})
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(append(os.Environ(), "PATH="+teardownStubs(t)+":"+os.Getenv("PATH")), tc.env...)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantOK {
				t.Fatalf("teardown err = %v, want success %v: %s", err, tc.wantOK, out)
			}
		})
	}
}

// A stop counts only when the user manager says the unit is down: an active unit
// or a manager that does not answer fails it.
func TestStopScriptNeedsAConfirmedStop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    []string
		wantOK bool
	}{
		{"unit inactive", nil, true},
		{"unit active", []string{"UNIT_STATE=active"}, false},
		{"manager does not answer", []string{"SYSTEMCTL_SHOW_EXIT=1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", detachedStopScript, "dyna-ci", filepath.Join(t.TempDir(), "d"), "unit.service")
			cmd.Env = append(append(os.Environ(), "PATH="+teardownStubs(t)+":"+os.Getenv("PATH")), tc.env...)
			if err := cmd.Run(); (err == nil) != tc.wantOK {
				t.Fatalf("stop err = %v, want success %v", err, tc.wantOK)
			}
		})
	}
}

// The systemd user manager has a PATH of its own, without the tools an owner
// installs for a login shell. A detached check must see the PATH a login shell
// of the node gives, as it did when it ran in an SSH login shell.
func TestDetachedCheckGetsTheLoginShellPath(t *testing.T) {
	dir := t.TempDir()
	stubs := nodeStubs(t)
	loginBin := filepath.Join(dir, "login-bin")
	if err := os.MkdirAll(loginBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loginBin, "owner-tool"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A login shell that prints a banner before it prints its PATH. It ignores
	// its arguments, so the host's profiles never change the PATH it reports.
	shell := filepath.Join(dir, "login-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\necho 'Welcome to the node'\nprintf '%s\\n' \""+loginBin+":$PATH\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// This systemd-run starts the unit with only the PATH the options give, as a
	// user manager does, and not the PATH of the session that called it.
	managerPath := "/usr/bin:/bin"
	runner := "#!/bin/sh\nunit_path=" + managerPath + "\nwhile [ $# -gt 0 ]; do\n  case \"$1\" in\n    --setenv=PATH=*) unit_path=${1#--setenv=PATH=} ;;\n    --) shift; break ;;\n  esac\n  shift\ndone\nPATH=$unit_path exec setsid -w \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stubs, "systemd-run"), []byte(runner), 0o700); err != nil {
		t.Fatal(err)
	}
	detached := filepath.Join(dir, "check", "detached")
	if err := startOnShell(t, stubs, detached, "command -v owner-tool\n", "SHELL="+shell); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(filepath.Join(detached, "exit.json"))
	if err != nil || !strings.Contains(string(record), `"exitCode":0`) {
		log, _ := os.ReadFile(filepath.Join(detached, "log"))
		t.Fatalf("the check did not find a login-shell tool: %q %v\nlog: %s", record, err, log)
	}
}
