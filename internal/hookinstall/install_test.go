package hookinstall

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/store"
	"dyna-agent/internal/userservices"
)

const testID = "11111111111111111111111111111111"
const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixture(t *testing.T) (Service, Job) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "DYNA_") {
			t.Cleanup(func() { _ = os.Setenv(key, value) })
			_ = os.Unsetenv(key)
		}
	}
	// A Go telemetry child can outlive the command and race temporary XDG cleanup.
	// GOTELEMETRY=off does not help: the toolchain ignores that variable and
	// reads the mode from goTelemetryDir's "mode" file. Without "off" there, a
	// real go build writes local/ and upload/ data and starts a detached
	// telemetry child that keeps writing after the test returns.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	telemetryDir := goTelemetryDir(t)
	if err := os.MkdirAll(telemetryDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(telemetryDir, "mode"), []byte("off"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(runtime, "no-bus"))
	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	c := DefaultConfig(filepath.Join(root, "main"), filepath.Join(root, "cli"))
	c.Build = filepath.Join(root, "build")
	c.Bin = filepath.Join(root, "bin")
	c.State = filepath.Join(root, "state", "installed-commit")
	c.Lock = filepath.Join(root, "install.lock")
	for _, path := range []string{c.Main, c.Bin} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	j := Job{ID: testID, SHA: testSHA, Repo: "example/repo", Hook: "install", HookLog: filepath.Join(root, "hook.log"), Config: c}
	if err := os.WriteFile(j.HookLog, []byte("initial hook log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := Service{Jobs: Store{Root: filepath.Join(root, "hooks", "install")}, Now: func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }}
	if err := s.Jobs.Update(j.ID, func(out *Job) error { *out = j; return nil }); err != nil {
		t.Fatal(err)
	}
	return s, j
}

// goTelemetryDir is the Go toolchain's telemetry directory. The toolchain
// ignores the GOTELEMETRY environment variable and reads its mode from the
// "mode" file in this directory instead (see "go help telemetry").
func goTelemetryDir(t *testing.T) string {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "go", "telemetry")
}

func fakeInstaller(t *testing.T, j Job, branch, fail string, calls *[]string) processguard.Executor {
	t.Helper()
	return func(ctx context.Context, p processguard.Process) error {
		command := strings.Join(p.Args, " ")
		*calls = append(*calls, command)
		if fail != "" && strings.Contains(command, fail) {
			return errors.New("fake failure")
		}
		// A build with web/ stands in for the engine build that has "dyna engine run".
		if strings.HasSuffix(command, "engine run --help") {
			if _, err := os.Stat(filepath.Join(j.Config.Build, "web", "package.json")); err != nil {
				return errors.New("unknown command \"engine\"")
			}
		}
		if len(p.Args) > 2 && p.Args[1] == "engine" && p.Args[2] == "install-services" {
			return userservices.Install(ctx, userservices.Options{
				Binary: p.Args[0], Dir: j.Config.Units, Environment: p.Env, Output: p.Output,
				RestartComputers: strings.Contains(command, "--restart-computers"),
				Run: func(_ context.Context, args ...string) (string, error) {
					cmd := strings.Join(args, " ")
					*calls = append(*calls, cmd)
					if fail != "" && strings.Contains(cmd, fail) {
						return "", errors.New("fake failure")
					}
					return "", nil
				},
			})
		}
		if strings.Contains(command, "rev-parse origin/main") {
			fmt.Fprintln(p.Output, testSHA)
		}
		if strings.Contains(command, "branch --show-current") {
			fmt.Fprintln(p.Output, branch)
		}
		if len(p.Args) > 5 && p.Args[3] == "go" {
			out := p.Args[len(p.Args)-2]
			return os.WriteFile(out, []byte("new dyna"), 0755)
		}
		if len(p.Args) > 4 && p.Args[3] == "./build.sh" {
			return os.WriteFile(p.Args[4], []byte("new desktop"), 0755)
		}
		return nil
	}
}

// TestInstallRequiresCommandLayout runs the real Go build. Every other
// installer command, including systemctl, uses the fake executor.
func TestInstallRequiresCommandLayout(t *testing.T) {
	for _, target := range []string{".", "./cmd/dyna"} {
		t.Run(target, func(t *testing.T) {
			s, j := fixture(t)
			source := filepath.Join(j.Config.Build, target)
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			for name, body := range map[string]string{
				filepath.Join(j.Config.Build, "go.mod"): "module example.invalid/installfixture\n\ngo 1.26.3\n",
				filepath.Join(source, "main.go"):        "package main\nfunc main() {}\n",
			} {
				if err := os.WriteFile(name, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var calls []string
			fake := fakeInstaller(t, j, "main", "", &calls)
			s.Exec = func(ctx context.Context, p processguard.Process) error {
				if len(p.Args) > 5 && p.Args[3] == "go" {
					p.Timeout = 30 * time.Second
					err := processguard.Execute(ctx, p)
					// The build must leave the mode file as the only entry in
					// the telemetry directory. Telemetry data here means a
					// detached child can outlive the test and race TempDir cleanup.
					entries, readErr := os.ReadDir(goTelemetryDir(t))
					names := make([]string, 0, len(entries))
					for _, entry := range entries {
						names = append(names, entry.Name())
					}
					if readErr != nil || !slices.Equal(names, []string{"mode"}) {
						t.Fatalf("real go build wrote telemetry data into the fixture: %v %v", names, readErr)
					}
					return err
				}
				return fake(ctx, p)
			}
			var output bytes.Buffer
			err := s.Locked(context.Background(), j.ID, "", &output)
			if target == "." {
				if err == nil {
					t.Fatal("installed a root-only tree after the cmd/dyna move")
				}
				if state, statErr := s.Jobs.installState(j.Config.State); statErr != nil || state.Installed != "" {
					t.Fatalf("failed root-only build advanced install state: %#v %v", state, statErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("install: %v\n%s", err, &output)
			}
			if !strings.Contains(strings.Join(calls, "\n"), "systemctl --user restart") {
				t.Fatal("install did not reach the fake service restart")
			}
		})
	}
}

func TestInstallPreservesStateAndBackups(t *testing.T) {
	s, j := fixture(t)
	var calls []string
	s.Exec = fakeInstaller(t, j, "main", "", &calls)
	for _, name := range []string{"dyna", "dyna-desktop"} {
		if err := os.WriteFile(filepath.Join(j.Config.Bin, name), []byte("old "+name), 0755); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 6; n++ {
			p := filepath.Join(j.Config.Bin, fmt.Sprintf("%s.bak-old-%d", name, n))
			if err := os.WriteFile(p, []byte("backup"), 0755); err != nil {
				t.Fatal(err)
			}
			past := time.Date(2020, 1, n+1, 0, 0, 0, 0, time.UTC)
			_ = os.Chtimes(p, past, past)
		}
	}
	var out bytes.Buffer
	if err := s.Locked(context.Background(), j.ID, "", &out); err != nil {
		t.Fatal(err)
	}
	state, err := s.Jobs.installState(j.Config.State)
	if err != nil || state.Installed != testSHA {
		t.Fatalf("state: %#v %v", state, err)
	}
	for name, want := range map[string]string{"dyna": "new dyna", "dyna-desktop": "new desktop"} {
		target := filepath.Join(j.Config.Bin, name)
		b, err := os.ReadFile(target)
		if err != nil || string(b) != want {
			t.Fatalf("binary %s: %s %v", name, b, err)
		}
		backups, _ := filepath.Glob(target + ".bak-*")
		if len(backups) != 5 {
			t.Fatalf("backups for %s: %d", name, len(backups))
		}
		backup, err := os.ReadFile(target + ".bak-" + s.now().Local().Format("20060102-150405"))
		if err != nil || string(backup) != "old "+name {
			t.Fatalf("backup: %s %v", backup, err)
		}
	}
	all := strings.Join(calls, "\n")
	for _, want := range []string{"merge -q --ff-only origin/main", "npm ci --silent", "npm run build", "go build -p 2", "./build.sh", "systemctl --user restart dyna-serve"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing command %s", want)
		}
	}
	calls = nil
	if err := s.Locked(context.Background(), j.ID, "", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "npm") {
		t.Fatal("rebuilt installed commit")
	}
}
func TestCheckoutWarningsPersistOnce(t *testing.T) {
	for _, tc := range []struct{ name, branch, fail, cause string }{{"branch", "feature", "", "branch:feature"}, {"dirty", "main", "diff --quiet", "dirty"}, {"index", "main", "diff --cached --quiet", "dirty"}, {"diverged", "main", "merge -q --ff-only", "diverged"}} {
		t.Run(tc.name, func(t *testing.T) {
			s, j := fixture(t)
			var calls []string
			s.Exec = fakeInstaller(t, j, tc.branch, tc.fail, &calls)
			if err := os.MkdirAll(filepath.Dir(j.Config.State), 0700); err != nil {
				t.Fatal(err)
			}
			if err := s.Jobs.saveInstallState(j.Config.State, installState{Installed: testSHA}); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				if err := s.Locked(context.Background(), j.ID, "", &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Jobs.Read(j.ID)
			if err != nil || len(got.Warnings) != 1 || got.Warnings[0].Cause != tc.cause {
				t.Fatalf("warnings: %#v %v", got, err)
			}
			state, err := s.Jobs.installState(j.Config.State)
			if err != nil || state.Alerted != tc.cause {
				t.Fatalf("state %#v: %v", state, err)
			}
			if tc.branch != "main" && strings.Contains(strings.Join(calls, "\n"), "merge -q") {
				t.Fatal("changed non-main checkout")
			}
		})
	}
}
func TestBuildOrRestartFailureDoesNotAdvanceState(t *testing.T) {
	for _, fail := range []string{"npm ci", "go build", "./build.sh", "systemctl --user restart"} {
		t.Run(fail, func(t *testing.T) {
			s, j := fixture(t)
			var calls []string
			s.Exec = fakeInstaller(t, j, "main", fail, &calls)
			err := s.Locked(context.Background(), j.ID, "", &bytes.Buffer{})
			if err == nil {
				t.Fatal("failure lost")
			}
			if state, err := s.Jobs.installState(j.Config.State); err != nil || state.Installed != "" {
				t.Fatalf("state advanced: %#v %v", state, err)
			}
		})
	}
}
func TestStartDetachesAndRecordsUnit(t *testing.T) {
	s, j := fixture(t)
	j.ID = "22222222222222222222222222222222"
	var args []string
	s.Exec = func(_ context.Context, p processguard.Process) error {
		args = p.Args
		job, err := s.Jobs.Read(j.ID)
		if err != nil || job.Status != "queued" {
			t.Fatalf("missing pre-launch receipt: %#v %v", job, err)
		}
		return nil
	}
	var log bytes.Buffer
	if err := s.Start(context.Background(), j, &log); err != nil {
		t.Fatal(err)
	}
	got, err := s.Jobs.Read(j.ID)
	if err != nil || got.Unit != "dyna-install-"+testSHA || got.Status != "queued" {
		t.Fatalf("job: %#v %v", got, err)
	}
	command := strings.Join(args, " ")
	for _, want := range []string{"systemd-run --user --unit dyna-install-" + testSHA, "RemainAfterExit=yes", "hooks _install-worker " + j.ID, "--jobs-root " + s.Jobs.Root} {
		if !strings.Contains(command, want) {
			t.Errorf("unit command missing %s: %s", want, command)
		}
	}
	if strings.Contains(command, "--scope") || strings.Contains(command, "--collect") {
		t.Fatalf("unit cannot survive/recover: %s", command)
	}
}
func TestRecoverKilledUnitAndAppendLogOnce(t *testing.T) {
	s, j := fixture(t)
	j.Status = "running"
	j.Unit = "dyna-install-" + testSHA
	j.WorkerLog = filepath.Join(t.TempDir(), "worker.log")
	if err := os.WriteFile(j.WorkerLog, []byte("partial build log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Jobs.Update(j.ID, func(out *Job) error { *out = j; return nil }); err != nil {
		t.Fatal(err)
	}
	s.Exec = func(_ context.Context, p processguard.Process) error {
		fmt.Fprint(p.Output, "LoadState=loaded\nActiveState=failed\nSubState=failed\nResult=signal\nExecMainStatus=9\n")
		return nil
	}
	got, err := s.InspectUnit(context.Background(), j)
	if err != nil || got.Result != "error" || got.Status != "finished" {
		t.Fatalf("unit: %#v %v", got, err)
	}
	for n := 0; n < 2; n++ {
		if err := Log(got); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(j.HookLog)
	if err != nil || strings.Count(string(b), "partial build log") != 1 || !strings.Contains(string(b), "result=signal") {
		t.Fatalf("readback: %s %v", b, err)
	}
}

func TestOlderQueuedUnitCannotDowngradeInstalledCommit(t *testing.T) {
	s, j := fixture(t)
	var calls []string
	// This receipt was queued for an older SHA, but a newer unit won the lock.
	j.SHA = strings.Repeat("b", 40)
	if err := s.Jobs.Update(j.ID, func(out *Job) error { *out = j; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(j.Config.State), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Jobs.saveInstallState(j.Config.State, installState{Installed: testSHA}); err != nil {
		t.Fatal(err)
	}
	s.Exec = fakeInstaller(t, j, "main", "", &calls)
	if err := s.Locked(context.Background(), j.ID, "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, command := range calls {
		if strings.Contains(command, "npm") || strings.Contains(command, "checkout") {
			t.Fatalf("older unit rebuilt: %s", command)
		}
	}
	got, err := s.Jobs.installState(j.Config.State)
	if err != nil || got.Installed != testSHA {
		t.Fatalf("downgraded: %#v %v", got, err)
	}
	if len(calls) == 0 || !strings.Contains(calls[0], "fetch -q origin main") {
		t.Fatal("worker did not refresh main under the lock")
	}
}

func TestWorkerUsesHookLogsAndLegacyFlock(t *testing.T) {
	s, j := fixture(t)
	called := false
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if p.Args[0] == "git" {
			command := strings.Join(p.Args, " ")
			switch {
			case strings.Contains(command, "--show-toplevel"):
				fmt.Fprintln(p.Output, j.Config.Main)
			case strings.Contains(command, "HEAD^{commit}"):
				fmt.Fprintln(p.Output, testSHA)
			case strings.Contains(command, "remote.origin.url"):
				fmt.Fprintln(p.Output, "https://example.org/repo.git")
			}
			return nil
		}
		called = true
		command := strings.Join(p.Args, " ")
		if !strings.Contains(command, "flock -w 1800 "+j.Config.Lock) || !strings.Contains(command, "hooks _install-locked "+j.ID) {
			t.Errorf("worker command: %s", command)
		}
		return nil
	}
	if err := s.Worker(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Jobs.Read(j.ID)
	if err != nil || !called || got.Result != "pass" || got.WorkerLog == "" {
		t.Fatalf("worker: %#v, called %t, %v", got, called, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Jobs.Root), "checks")); !os.IsNotExist(err) {
		t.Fatalf("worker created checks data: %v", err)
	}
	rel, err := filepath.Rel(s.Jobs.Root, got.WorkerLog)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("worker log is outside the hooks store: %s", got.WorkerLog)
	}
}

func TestSameLockInteroperatesWithLegacyFlock(t *testing.T) {
	s, j := fixture(t)
	_ = s
	func() {
		unlock, err := store.Lock(j.Config.Lock)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		err = processguard.Execute(context.Background(), processguard.Process{Dir: j.Config.Main, Args: []string{"flock", "-w", "0", j.Config.Lock, "true"}, Env: processguard.MetadataEnv(), Timeout: time.Second, Output: &bytes.Buffer{}})
		if err == nil {
			t.Fatal("legacy flock did not contend with the install lock")
		}
	}()
	if err := processguard.Execute(context.Background(), processguard.Process{Dir: j.Config.Main, Args: []string{"flock", "-w", "0", j.Config.Lock, "true"}, Env: processguard.MetadataEnv(), Timeout: time.Second, Output: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig("/home/user/dyna-agent", "/home/user/.local/bin/dyna")
	if c.Lock != filepath.Join(store.DataDir(), "hooks", "install", "install.lock") || c.Build != "/home/user/dyna-agent-autoinstall" || c.State != filepath.Join(os.Getenv("HOME"), ".local/share/dyna-sync/installed-commit") {
		t.Fatalf("legacy paths: %#v", c)
	}
}
func TestInstallSwitchesToEngineUnit(t *testing.T) {
	s, j := fixture(t)
	var calls []string
	s.Exec = fakeInstaller(t, j, "main", "", &calls)
	if err := os.MkdirAll(filepath.Join(j.Config.Build, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.Config.Build, "web", "package.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(j.Config.Units, 0700); err != nil {
		t.Fatal(err)
	}
	serve := filepath.Join(j.Config.Units, "dyna-serve.service")
	if err := os.WriteFile(serve, []byte("[Service]\nExecStart=/bin/dyna-desktop serve --addr 127.0.0.1:9000 --route https://example.test,https://cards.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stale := "[Service]\nExecStart=/tmp/deleted-test-binary engine run\n"
	if err := os.WriteFile(filepath.Join(j.Config.Units, "dyna-engine.service"), []byte(stale), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Locked(context.Background(), j.ID, "", &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(serve); !os.IsNotExist(err) {
		t.Fatalf("old serve unit kept: %v", err)
	}
	unit, err := os.ReadFile(filepath.Join(j.Config.Units, "dyna-engine.service"))
	if err != nil || !strings.Contains(string(unit), "engine run --allow-agent") {
		t.Fatalf("engine unit: %s %v", unit, err)
	}
	if !strings.Contains(out.String(), "old service used --addr --route") || !strings.Contains(out.String(), "pass these flags to dyna engine run") {
		t.Fatalf("missing old flag warning: %s", out.String())
	}
	all := strings.Join(calls, "\n")
	for _, want := range []string{"disable --now dyna-serve", "daemon-reload", "restart dyna-engine.service"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing command %s", want)
		}
	}
	if strings.Contains(all, "restart dyna-serve") {
		t.Error("restarted the old serve unit")
	}
}

func TestComputerRestartRuleUsesInstalledRevision(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%v", changed), func(t *testing.T) {
			s, j := fixture(t)
			old := strings.Repeat("b", 40)
			var calls []string
			s.Exec = func(_ context.Context, p processguard.Process) error {
				calls = append(calls, strings.Join(p.Args, " "))
				if changed {
					fmt.Fprintln(p.Output, "internal/computers/process_linux.go")
				}
				return nil
			}
			d := driver{s, j.Config, &bytes.Buffer{}}
			got, err := d.computersChanged(context.Background(), old, testSHA)
			if err != nil || got != changed {
				t.Fatalf("changed=%v err=%v", got, err)
			}
			if len(calls) != 1 || !strings.Contains(calls[0], "diff --name-only "+old+" "+testSHA+" -- ") {
				t.Fatalf("restart did not compare installed revision: %v", calls)
			}
			for _, path := range computerCodePaths {
				if !strings.Contains(calls[0], path) {
					t.Fatalf("restart omitted %s: %v", path, calls)
				}
			}
		})
	}
}

func TestFirstInstallHasNoComputerRestartBaseline(t *testing.T) {
	s, j := fixture(t)
	s.Exec = func(context.Context, processguard.Process) error {
		t.Fatal("first install requested a git diff without a prior revision")
		return nil
	}
	if changed, err := (driver{s, j.Config, &bytes.Buffer{}}).computersChanged(context.Background(), "", testSHA); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}

func TestHookInstallerRefusesRealCommandsInGoTests(t *testing.T) {
	if err := New().Exec(context.Background(), processguard.Process{Args: []string{"systemctl", "--user", "restart", "dyna-engine"}, Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "Go tests") {
		t.Fatalf("hook installer guard: %v", err)
	}
}

var updateGenerated = flag.Bool("update", false, "rewrite computer_paths_gen.go")

// TestComputerCodePathsGenerated fails when computer_paths_gen.go no longer
// matches the import graph of internal/computers. Run go generate to fix it.
func TestComputerCodePathsGenerated(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "go", "list", "-deps", "-f", `{{.ImportPath}}{{$dir := .Dir}}{{range .GoFiles}} {{$dir}}/{{.}}{{end}}`, "dyna-agent/internal/computers").CombinedOutput()
	if err != nil {
		t.Fatalf("list computer dependencies: %v: %s", err, output)
	}
	// The daemon's entry point imports most of the CLI, so only its own package counts.
	paths := []string{"go.mod", "go.sum", "internal/cli/computer"}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		path, ok := strings.CutPrefix(fields[0], "dyna-agent/")
		if !ok {
			continue
		}
		// Reading the sources ties the Go test cache to them, so a new import reruns this test.
		for _, file := range fields[1:] {
			if _, err := os.ReadFile(file); err != nil {
				t.Fatal(err)
			}
		}
		paths = append(paths, path)
	}
	slices.Sort(paths)
	var src bytes.Buffer
	src.WriteString("// Code generated by \"go test -run TestComputerCodePathsGenerated . -args -update\"; DO NOT EDIT.\n\n")
	src.WriteString("package hookinstall\n\n")
	src.WriteString("// computerCodePaths is the one restart boundary for the computer daemon: the\n")
	src.WriteString("// packages internal/computers imports, its entry point, and the module files.\n")
	src.WriteString("// Unrelated landings preserve apps.\n")
	src.WriteString("var computerCodePaths = []string{\n")
	for _, path := range paths {
		fmt.Fprintf(&src, "\t%q,\n", path)
	}
	src.WriteString("}\n")
	if *updateGenerated {
		if err := os.WriteFile("computer_paths_gen.go", src.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile("computer_paths_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, src.Bytes()) {
		t.Fatalf("computer_paths_gen.go is stale; run go generate ./internal/hookinstall\nwant:\n%s", src.Bytes())
	}
}

func TestInstallStampsKnownCommitInExistingBuild(t *testing.T) {
	s, j := fixture(t)
	var calls []string
	s.Exec = fakeInstaller(t, j, "main", "", &calls)
	if err := s.Locked(context.Background(), j.ID, "", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	builds := 0
	for _, command := range calls {
		if strings.Contains(command, "go build") {
			builds++
			if !strings.Contains(command, "-ldflags -X dyna-agent/internal/cli/version.buildRevision="+testSHA) {
				t.Fatalf("build does not stamp the installed commit: %s", command)
			}
		}
	}
	if builds != 1 {
		t.Fatalf("build count = %d, want 1", builds)
	}
}
