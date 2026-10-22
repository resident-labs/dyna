package processguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fixture(mode, root string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessGuardFixture$")
	cmd.Env = append(os.Environ(), "PG_FIXTURE="+mode, "PG_ROOT="+root)
	return cmd
}

// The runner and vendor are separate OS processes. No real accounts, tools,
// vendor programs, or persistent resource roots are used in these tests.
func TestProcessGuardFixture(t *testing.T) {
	mode, root := os.Getenv("PG_FIXTURE"), os.Getenv("PG_ROOT")
	switch mode {
	case "":
		return
	case "runner":
		c, err := Wrap(fixture("tree", root))
		if err != nil {
			os.Exit(101)
		}
		err = c.Start(func(r Reference) error {
			b, e := json.Marshal(r)
			if e != nil {
				return e
			}
			return os.WriteFile(filepath.Join(root, "ref"), b, 0600)
		})
		if err != nil {
			os.Exit(102)
		}
		if c.Wait() != nil {
			os.Exit(103)
		}
		os.Exit(0)
	case "tree", "background":
		// This shell owns a background sleep and repeatedly writes tokens.
		cmd := exec.Command("/bin/sh", "-c", `sleep 60 & echo $! > "$PG_ROOT/sleep"; while :; do printf x >> "$PG_ROOT/tokens"; sleep 0.01; done`)
		if err := cmd.Start(); err != nil {
			os.Exit(104)
		}
		_ = os.WriteFile(filepath.Join(root, "child"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
		if mode == "background" {
			deadline := time.Now().Add(2 * time.Second)
			for (!fileExists(filepath.Join(root, "sleep")) || !fileExists(filepath.Join(root, "tokens"))) && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			os.Exit(37)
		}
		select {}
	case "bytes":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		_, _ = os.Stderr.Write([]byte{0, 1, 2, 255, '\n'})
		os.Exit(41)
	case "fds":
		entries, _ := os.ReadDir("/proc/self/fd")
		for _, e := range entries {
			target, err := os.Readlink("/proc/self/fd/" + e.Name())
			if err == nil {
				_, _ = os.Stdout.WriteString(e.Name() + " " + target + "\n")
			}
		}
		_, _ = os.Stdout.WriteString("MARKER=" + os.Getenv("DYNA_SUBAGENT_LOCK_FD") + "\n")
		os.Exit(0)
	case "signal", "group-signal":
		n, _ := strconv.Atoi(os.Getenv("PG_SIGNAL"))
		sig := syscall.Signal(n)
		if err := prepareSignalReplay(sig); err != nil {
			os.Exit(106)
		}
		// Check the effective settings after exec, before either fatal signal.
		if err := recordDumpProof(root); err != nil {
			os.Exit(107)
		}
		if mode == "group-signal" {
			select {}
		}
		_ = syscall.Kill(os.Getpid(), sig)
		select {}
	case "replay":
		n, _ := strconv.Atoi(os.Getenv("PG_SIGNAL"))
		// Exercise production replay without any test-side suppression first.
		// Core=true must not allow the guard to create another core dump.
		exitStatus(message{Signal: n, Core: true})
	default:
		os.Exit(105)
	}
}

func wrapTest(t *testing.T, cmd *exec.Cmd) *Controller {
	t.Helper()
	c, err := Wrap(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func await(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out: " + what)
}
func waitTest(t *testing.T, c *Controller) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(8 * time.Second):
		t.Fatal("Wait did not drain group")
		return nil
	}
}
func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func deadReference(t *testing.T, r Reference) {
	t.Helper()
	alive, err := Probe(r)
	if err != nil || alive {
		t.Fatalf("group still live: %v, %v", alive, err)
	}
}

func TestByteStreamsAndExactExit(t *testing.T) {
	cmd := fixture("bytes", t.TempDir())
	payload := bytes.Repeat([]byte{0, 255, 1, '\n', 'x'}, 200000)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	c := wrapTest(t, cmd)
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	err := waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 41 {
		t.Fatalf("exit: %v", err)
	}
	if !bytes.Equal(stdout.Bytes(), payload) {
		t.Fatalf("stdout changed: got %d bytes", stdout.Len())
	}
	if !bytes.Equal(stderr.Bytes(), []byte{0, 1, 2, 255, '\n'}) {
		t.Fatalf("stderr changed: %q", stderr.Bytes())
	}
	deadReference(t, c.Reference())
}

func TestBindBeforeNativeAndBindFailure(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", `echo vendor > "$1"`, "sh", filepath.Join(root, "native"))
	c := wrapTest(t, cmd)
	failure := errors.New("bind rejected")
	err := c.Start(func(r Reference) error {
		if fileExists(filepath.Join(root, "native")) {
			t.Error("native ran before bind")
		}
		if live, err := Probe(r); !live || err != nil {
			t.Errorf("bind reference is not live: %v %v", live, err)
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Start: %v", err)
	}
	if fileExists(filepath.Join(root, "native")) {
		t.Fatal("native ran after failed bind")
	}
	deadReference(t, c.Reference())
}

func TestNormalExitKillsBackgroundGrandchild(t *testing.T) {
	root := t.TempDir()
	c := wrapTest(t, fixture("background", root))
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	err := waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 {
		t.Fatalf("native exit status: %v", err)
	}
	deadReference(t, c.Reference())
	assertTokensStopped(t, root)
}

func assertTokensStopped(t *testing.T, root string) {
	t.Helper()
	before, _ := os.ReadFile(filepath.Join(root, "tokens"))
	time.Sleep(40 * time.Millisecond)
	after, _ := os.ReadFile(filepath.Join(root, "tokens"))
	if !bytes.Equal(before, after) {
		t.Fatal("background grandchild still writes tokens")
	}
}

func TestRunnerAndGuardianSIGKILL(t *testing.T) {
	for _, target := range []string{"runner", "guardian"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			cmd := fixture("runner", root)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			await(t, "native grandchild", func() bool {
				return fileExists(filepath.Join(root, "sleep")) && fileExists(filepath.Join(root, "tokens"))
			})
			data, err := os.ReadFile(filepath.Join(root, "ref"))
			if err != nil {
				t.Fatal(err)
			}
			var ref Reference
			if err := json.Unmarshal(data, &ref); err != nil {
				t.Fatal(err)
			}
			pid := cmd.Process.Pid
			if target == "guardian" {
				pid = ref.value.Guardian
			}
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				t.Fatalf("runner did not exit: %s", stderr.String())
			}
			await(t, "group drain", func() bool { live, e := Probe(ref); return e == nil && !live })
			assertTokensStopped(t, root)
			// The group includes the native vendor, shell, sleep, and token writer.
			for _, name := range []string{"child", "sleep"} {
				b, e := os.ReadFile(filepath.Join(root, name))
				if e != nil {
					t.Fatal(e)
				}
				p, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				if f, e := procFieldsForTest(p); e == nil && f != "Z" && f != "X" {
					t.Fatalf("%s still alive: %d %s", name, p, f)
				}
			}
		})
	}
}

func TestSignalsAndReferenceRoundTrip(t *testing.T) {
	c := wrapTest(t, exec.Command("/bin/sh", "-c", "exec sleep 60"))
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c.Reference())
	if err != nil {
		t.Fatal(err)
	}
	var ref Reference
	if err := json.Unmarshal(b, &ref); err != nil {
		t.Fatal(err)
	}
	bad := ref
	bad.value.Nonce = strings.Repeat("0", 64)
	if err := Signal(bad, syscall.SIGKILL); err == nil {
		t.Fatal("bad nonce authorized signal")
	}
	if live, err := Probe(ref); !live || err != nil {
		t.Fatalf("probe: %v %v", live, err)
	}
	if err := Signal(ref, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Fatalf("signal status changed: %v", err)
	}
	deadReference(t, ref)
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := wrapTest(t, exec.CommandContext(ctx, "/bin/sh", "-c", "exec sleep 60"))
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := waitTest(t, c); err == nil {
		t.Fatal("cancel succeeded")
	}
	deadReference(t, c.Reference())
}

func TestRejectExtraFiles(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command("/bin/true")
	cmd.ExtraFiles = []*os.File{f}
	if _, err := Wrap(cmd); err == nil {
		t.Fatal("ExtraFiles accepted")
	}
}

func TestFailedStartCanWait(t *testing.T) {
	for _, kind := range []string{"lookup", "native-exec", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			cmd := exec.Command("/no/such/processguard-native")
			if kind == "lookup" {
				cmd = exec.Command("no-such-processguard-executable-12345")
			}
			if kind == "canceled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				cmd = exec.CommandContext(ctx, "/bin/true")
			}
			c := wrapTest(t, cmd)
			if err := c.Start(nil); err == nil {
				t.Fatal("Start unexpectedly succeeded")
			}
			if err := waitTest(t, c); err == nil {
				t.Fatal("Wait lost Start error")
			}
			if c.Reference().value.Group > 1 {
				deadReference(t, c.Reference())
			}
		})
	}
}

func TestCancelDuringBindKeepsNativeGateClosed(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `echo native > "$1"`, "sh", filepath.Join(root, "native"))
	c := wrapTest(t, cmd)
	bound := make(chan Reference, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.Start(func(r Reference) error { bound <- r; <-release; return nil }) }()
	ref := <-bound
	cancel()
	await(t, "canceled bind group drain", func() bool { live, err := Probe(ref); return err == nil && !live })
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled Start succeeded")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("canceled Start blocked")
	}
	if err := waitTest(t, c); err == nil {
		t.Fatal("Wait lost canceled bind error")
	}
	if fileExists(filepath.Join(root, "native")) {
		t.Fatal("native ran after cancellation in bind")
	}
}

func TestBirthMismatchNeverSignalsReusedGroup(t *testing.T) {
	c := wrapTest(t, exec.Command("/bin/sh", "-c", "exec sleep 60"))
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	stale := c.Reference()
	stale.value.Birth = "different-birth"
	stale.value.Socket = filepath.Join(t.TempDir(), "missing")
	fake := &Controller{ref: stale}
	fake.cleanupFallback()
	if live, err := Probe(stale); err != nil || live {
		t.Fatalf("stale reference: %v %v", live, err)
	}
	if err := Signal(stale, syscall.SIGKILL); err == nil {
		t.Fatal("stale signal succeeded")
	}
	if live, err := Probe(c.Reference()); err != nil || !live {
		t.Fatalf("current group was affected: %v %v", live, err)
	}
	if err := c.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	err := waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("SIGKILL status: %v", err)
	}
	deadReference(t, c.Reference())
}

func TestHardContextCancelWithIgnoredTERM(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `trap '' TERM; echo ready > "$1"; while :; do sleep 60; done`, "sh", filepath.Join(root, "ready"))
	c := wrapTest(t, cmd)
	if err := c.SetCancelSignal(syscall.SIGINT); err == nil {
		t.Fatal("invalid cancel signal accepted")
	}
	if err := c.SetCancelSignal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SetCancelSignal(syscall.SIGTERM); err == nil {
		t.Fatal("late cancel signal accepted")
	}
	await(t, "TERM handler", func() bool { return fileExists(filepath.Join(root, "ready")) })
	cancel()
	err := waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("hard cancel status: %v", err)
	}
	deadReference(t, c.Reference())
}

func TestNativeFatalSignalHasExactStatusAndNoHelperOutput(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGPIPE, syscall.SIGUSR1} {
		t.Run(strconv.Itoa(int(sig)), func(t *testing.T) {
			root := t.TempDir()
			started := time.Now()
			cmd := fixture("signal", root)
			cmd.Dir = root
			cmd.Env = append(cmd.Env, "PG_SIGNAL="+strconv.Itoa(int(sig)))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			c := wrapTest(t, cmd)
			if err := c.Start(nil); err != nil {
				t.Fatal(err)
			}
			err := waitTest(t, c)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != sig {
				t.Fatalf("native signal %d changed: %v stderr=%s", sig, err, stderr.String())
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("helper output leaked: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			assertNoCoreStatus(t, exit)
			pid := assertDumpProof(t, root)
			assertNoCrashEvidence(t, started, root, pid, c.Reference().value.Guardian)
		})
	}
}

func TestOtherGroupTerminationSignal(t *testing.T) {
	root := t.TempDir()
	started := time.Now()
	cmd := fixture("group-signal", root)
	cmd.Env = append(cmd.Env, "PG_SIGNAL="+strconv.Itoa(int(syscall.SIGXCPU)))
	cmd.Dir = root
	c := wrapTest(t, cmd)
	if err := c.Start(nil); err != nil {
		t.Fatal(err)
	}
	await(t, "dump suppression after native exec", func() bool { return fileExists(filepath.Join(root, "dump-proof")) })
	pid := assertDumpProof(t, root)
	if err := c.Signal(syscall.SIGXCPU); err != nil {
		t.Fatal(err)
	}
	err := waitTest(t, c)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGXCPU {
		t.Fatalf("group SIGXCPU status: %v", err)
	}
	deadReference(t, c.Reference())
	assertNoCoreStatus(t, exit)
	assertNoCrashEvidence(t, started, root, pid, c.Reference().value.Guardian)
}

func TestSignalReplayNeverDumpsCore(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGXCPU} {
		t.Run(strconv.Itoa(int(sig)), func(t *testing.T) {
			root := t.TempDir()
			started := time.Now()
			cmd := fixture("replay", root)
			cmd.Dir = root
			cmd.Env = append(cmd.Env, "PG_SIGNAL="+strconv.Itoa(int(sig)))
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != sig {
				t.Fatalf("replay signal: %v", err)
			}
			assertNoCoreStatus(t, exit)
			assertNoCrashEvidence(t, started, root, cmd.Process.Pid)
		})
	}
}

func assertNoCoreStatus(t *testing.T, exit *exec.ExitError) {
	t.Helper()
	if exit.Sys().(syscall.WaitStatus).CoreDump() {
		t.Fatal("kernel entered the core-dump path")
	}
}

type dumpProof struct {
	PID      int
	Limit    syscall.Rlimit
	Dumpable int
}

func recordDumpProof(root string) error {
	proof := dumpProof{PID: os.Getpid()}
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &proof.Limit); err != nil {
		return err
	}
	var err error
	proof.Dumpable, err = dumpableForTest()
	if err != nil {
		return err
	}
	if proof.Limit.Cur != 0 || proof.Limit.Max != 0 || proof.Dumpable != 0 {
		return errors.New("core dump suppression ineffective")
	}
	b, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	pending := filepath.Join(root, "dump-proof.pending")
	if err := os.WriteFile(pending, b, 0600); err != nil {
		return err
	}
	return os.Rename(pending, filepath.Join(root, "dump-proof"))
}
func assertDumpProof(t *testing.T, root string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "dump-proof"))
	if err != nil {
		t.Fatal(err)
	}
	var proof dumpProof
	if err := json.Unmarshal(b, &proof); err != nil {
		t.Fatal(err)
	}
	if proof.Limit.Cur != 0 || proof.Limit.Max != 0 || proof.Dumpable != 0 {
		t.Fatalf("unsafe child dump settings: %+v", proof)
	}
	return proof.PID
}

func assertNoCrashEvidence(t *testing.T, started time.Time, root string, pids ...int) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "dump-proof" {
			t.Fatalf("unexpected fatal-test artifact: %s", entry.Name())
		}
	}
	assertNoCrashEvents(t, started, pids...)
}
