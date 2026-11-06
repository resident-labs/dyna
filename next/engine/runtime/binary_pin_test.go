package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type nativePinnedRunner struct {
	entered, release, finished string
	noSystemd                  bool
}

func (r *nativePinnedRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "-O" || a == "-M" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	if r.entered != "" && strings.Contains(command, "'dyna-link'") {
		prefix := fmt.Sprintf(": > %q; while [ ! -f %q ]; do sleep 0.01; done; ", r.entered, r.release)
		command = strings.Replace(command, `exec 9> .activation.lock`, prefix+`exec 9> .activation.lock`, 1)
		command += fmt.Sprintf("; status=$?; : > %q; exit $status", r.finished)
	}
	if r.noSystemd && strings.Contains(command, "dyna-binary %s") {
		command = strings.ReplaceAll(command, "systemctl --user show dyna.slice -p ControlGroup --value", `printf ""`)
	}
	// As with SSH, the remote command can survive losing the local transport.
	return (podman.ExecRunner{}).Run(ctx, in, "bash", "-c", `setsid bash -c "$1" <&0 & wait`, "native-ssh", command)
}
func stagePinnedFixture(t *testing.T, bin string, raw []byte) string {
	t.Helper()
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(bin, key), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, key, "dyna"), raw, 0755); err != nil {
		t.Fatal(err)
	}
	return key
}
func waitPinnedFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("binary barrier not reached: %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestPinnedContainerActuallyExecutesEngineBinaryDuringDowngradedWindow(t *testing.T) {
	tools, root := t.TempDir(), t.TempDir()
	layout := box.Layout{HostData: root}
	bin := layout.HostBin()
	gate, actual, pidFile := filepath.Join(tools, "release"), filepath.Join(tools, "actual"), filepath.Join(tools, "pid")
	oldSum := stagePinnedFixture(t, bin, []byte("#!/bin/bash\nprintf OLD > \"$PIN_ACTUAL\"\n"))
	newSum := stagePinnedFixture(t, bin, []byte("#!/bin/bash\nprintf NEW > \"$PIN_ACTUAL\"\n"))
	b := &Boxes{Layout: layout}
	b.binOnce.Do(func() { b.binSum = newSum })
	_, container := b.container(lifecycle.BoxSpec{Box: "b"}, "image", box.Mount{})
	var executable string
	for _, m := range container.Mounts {
		if m.BoxPath == "/opt/dyna/bin/current" {
			if !m.ReadOnly {
				t.Fatal("binary pin is writable")
			}
			executable = filepath.Join(m.HostPath, "dyna")
		}
		if m.BoxPath == "/opt/dyna/bin" {
			executable = filepath.Join(m.HostPath, "current", "dyna")
		}
	}
	if executable == "" {
		t.Fatal("container has no binary mount")
	}
	script := `#!/bin/bash
setsid bash -c 'while [ ! -f "$PIN_GATE" ]; do sleep 0.01; done; exec "$PIN_EXEC"' </dev/null >/dev/null 2>&1 9>&- &
printf '%s' "$!" > "$PIN_PID"
`
	if err := os.WriteFile(filepath.Join(tools, "podman"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	t.Setenv("PIN_GATE", gate)
	t.Setenv("PIN_ACTUAL", actual)
	t.Setenv("PIN_PID", pidFile)
	t.Setenv("PIN_EXEC", executable)
	t.Cleanup(func() {
		_ = os.WriteFile(gate, nil, 0600)
		raw, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(string(raw))
		if pid > 0 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
	h, err := podman.New("user@host", t.TempDir(), "host", &nativePinnedRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ActivateBinary(context.Background(), bin, newSum); err != nil {
		t.Fatal(err)
	}
	if err := h.StartContainer(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
	// Start has acknowledged tini, but its child has not resolved the executable yet.
	if err := h.ActivateBinary(context.Background(), bin, oldSum); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for {
		raw, err = os.ReadFile(actual)
		if err == nil && len(raw) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("container child never executed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if string(raw) != "NEW" {
		t.Fatalf("actual container child executed %s instead of pinned NEW after host current was downgraded", raw)
	}
}

func TestOneHostSurveyConvergesAfterUnconfirmedActivationAndRepeatedSHAABA(t *testing.T) {
	for _, oldRequests := range []int{1, 2} {
		t.Run(fmt.Sprintf("old-requests-%d", oldRequests), func(t *testing.T) {
			root, tools := t.TempDir(), t.TempDir()
			layout := box.Layout{HostData: root}
			bin := layout.HostBin()
			oldSum, newSum := stagePinnedFixture(t, bin, []byte("old-engine")), stagePinnedFixture(t, bin, []byte("new-engine"))
			var old []*nativePinnedRunner
			for i := 0; i < oldRequests; i++ {
				r := &nativePinnedRunner{entered: filepath.Join(tools, fmt.Sprintf("entered-%d", i)), release: filepath.Join(tools, fmt.Sprintf("release-%d", i)), finished: filepath.Join(tools, fmt.Sprintf("done-%d", i))}
				old = append(old, r)
				t.Cleanup(func() { _ = os.WriteFile(r.release, nil, 0600) })
				h, err := podman.New("user@host", t.TempDir(), "host", r)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- h.ActivateBinary(ctx, bin, oldSum) }()
				waitPinnedFile(t, r.entered)
				cancel()
				if err := <-done; err == nil {
					t.Fatalf("lost activation transport unexpectedly succeeded: %v", err)
				}
			}
			newest, err := podman.New("user@host", t.TempDir(), "host", &nativePinnedRunner{noSystemd: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := newest.ActivateBinary(context.Background(), bin, newSum); err != nil {
				t.Fatal(err)
			}
			for _, r := range old {
				if err := os.WriteFile(r.release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				waitPinnedFile(t, r.finished)
			}
			if target, err := os.Readlink(filepath.Join(bin, "current")); err != nil || target != oldSum {
				t.Fatalf("late activation did not reproduce the downgraded window: %s, %v", target, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = state.Close() })
			ticks := make(chan time.Time, 1)
			b := &Boxes{Store: restoreStore{hosts: engineHosts("host")}, Layout: layout, Version: "test", Context: ctx, Pressure: engine.New(state, nil, nil, time.Now), heartbeatTicks: ticks}
			b.binOnce.Do(func() { b.binSum = newSum })
			b.mu.Lock()
			b.cache("host", newest)
			b.mu.Unlock()
			ticks <- time.Now()
			deadline := time.Now().Add(8 * time.Second)
			for {
				b.mu.Lock()
				load, seen := b.loads["host"]
				b.mu.Unlock()
				if seen && load.summary.Health == contract.HostHealthy {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("the single production survey callback did not complete")
				}
				time.Sleep(5 * time.Millisecond)
			}
			deadline = time.Now().Add(8 * time.Second)
			for {
				target, err := os.Readlink(filepath.Join(bin, "current"))
				if err == nil && target == newSum {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("one survey did not restore NEW: %s, %v", target, err)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
