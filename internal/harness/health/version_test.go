package health

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/profile"
)

func versionFake(t *testing.T, name, body string) (string, profile.Profile) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := fsutil.WriteExecutable(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path, profile.Profile{Harness: profile.HarnessPi, RuntimeExecutable: path, Env: map[string]string{"HOME": t.TempDir()}}
}

// The default two-second deadline is not under test in these tests. Starting
// a shell script can exceed it on a loaded host, so they use a long deadline.
// The private copy is checked; a pi on PATH is never run.
func TestDetectVersionUsesPrivateExecutable(t *testing.T) {
	host, _ := versionFake(t, "pi", "exit 99")
	t.Setenv("PATH", filepath.Dir(host))
	path, p := versionFake(t, "pi", `[ "$#" = 1 ] && [ "$1" = --version ] || exit 98
printf 'pi-cli 1.2.3\n'`)
	got := detectVersion(context.Background(), p, time.Minute)
	if got.Path != path || !got.Installed || got.Version != "1.2.3" || got.Error != "" {
		t.Fatalf("unexpected discovery: %+v", got)
	}
}

func TestDetectVersionRefreshAfterBinaryChange(t *testing.T) {
	path, p := versionFake(t, "pi", "printf '1.2.3\\n'")
	first := detectVersion(context.Background(), p, time.Minute)
	if first.Version != "1.2.3" {
		t.Fatalf("initial version: %+v", first)
	}
	// A different size invalidates the entry even on a coarse timestamp filesystem.
	if err := fsutil.WriteExecutable(path, []byte("#!/bin/sh\nprintf '20.30.40\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got := detectVersion(context.Background(), p, time.Minute)
	if got.Version != "20.30.40" || got.Error != "" {
		t.Fatalf("stale version: %+v", got)
	}
}

func TestDetectVersionRefreshAfterMtimeChange(t *testing.T) {
	path, p := versionFake(t, "pi", "printf '1.2.3\\n'")
	if got := detectVersion(context.Background(), p, time.Minute); got.Version != "1.2.3" {
		t.Fatalf("initial version: %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve size, but force a distinct timestamp even on coarse filesystems.
	if err := fsutil.WriteExecutable(path, []byte("#!/bin/sh\nprintf '4.5.6\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	if got := detectVersion(context.Background(), p, time.Minute); got.Version != "4.5.6" {
		t.Fatalf("mtime ignored: %+v", got)
	}
}

// A version command that exits while a descendant still holds its output
// open is a good run. With pipes, Run waited for that output and the run
// came back as "version command failed"; a late copy goroutine on a loaded
// CI host gave the same failure (TestDetectVersionCacheAndEnvironmentChange).
func TestDetectVersionIgnoresDescendantHoldingOutput(t *testing.T) {
	_, p := versionFake(t, "pi", `printf 'pi-cli 1.2.3\n'
/bin/sleep 5 &`)
	start := time.Now()
	got := detectVersion(context.Background(), p, time.Minute)
	if got.Version != "1.2.3" || got.Error != "" {
		t.Fatalf("version with a descendant holding stdout: %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s for the descendant", elapsed)
	}
}

func TestDetectVersionCacheAndEnvironmentChange(t *testing.T) {
	path, p := versionFake(t, "pi", `printf x >> "$COUNT_FILE"
printf '%s\n' "$FAKE_VERSION"`)
	count := filepath.Join(t.TempDir(), "calls")
	p.Env["COUNT_FILE"], p.Env["FAKE_VERSION"] = count, "1.2.3"
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got := detectVersion(context.Background(), p, time.Minute)
			if got.Version != "1.2.3" {
				t.Errorf("cache result: %+v", got)
			}
		})
	}
	wg.Wait()
	data, err := os.ReadFile(count)
	if err != nil || string(data) != "x" {
		t.Fatalf("expected one execution: %q, %v", data, err)
	}
	p.Env["FAKE_VERSION"] = "2.3.4"
	if got := detectVersion(context.Background(), p, time.Minute); got.Version != "2.3.4" {
		t.Fatalf("environment ignored: %+v", got)
	}
	versions.Lock()
	for key, entry := range versions.entries {
		if key.path == path {
			entry.expires = time.Now().Add(-time.Second)
		}
	}
	versions.Unlock()
	if got := detectVersion(context.Background(), p, time.Minute); got.Version != "2.3.4" {
		t.Fatalf("expired cache result: %+v", got)
	}
	data, err = os.ReadFile(count)
	if err != nil || string(data) != "xxx" {
		t.Fatalf("expected refresh: %q, %v", data, err)
	}
}

// Without a private copy, the result says whether Dyna can install one.
func TestDetectVersionMissingPrivateCopy(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	previous := HarnessReady
	t.Cleanup(func() { HarnessReady = previous })
	for _, installable := range []bool{true, false} {
		HarnessReady = func(context.Context, string, string) (bool, error) {
			if installable {
				return false, nil
			}
			return false, errors.New("Install pi on the Harnesses page first.")
		}
		p := profile.Profile{Harness: profile.HarnessPi, Env: map[string]string{"HOME": t.TempDir()}}
		got := detectVersion(context.Background(), p, time.Minute)
		if got.Installed || got.Path != "" || got.Installable != installable || got.Error == "" {
			t.Fatalf("installable=%v: %+v", installable, got)
		}
		if !installable && got.Error != "Install pi on the Harnesses page first." {
			t.Fatalf("missing fix in error: %q", got.Error)
		}
	}
}

func TestDetectVersionMockNeverExecutes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	_, p := versionFake(t, "codex", `printf 'executed' > "$MARKER"`)
	p.Harness, p.Env["MARKER"] = profile.HarnessMock, marker
	got := detectVersion(context.Background(), p, time.Minute)
	if got.Version != "built-in" || !got.Installed || got.Path != "" || got.Error != "" {
		t.Fatalf("mock result: %+v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command executed: %v", err)
	}
}

// A run that ends while a descendant still runs must kill the whole process
// group. The descendant holds a pipe open for its whole life, so its closed
// pipe end proves the kill reached it: no fixed wait decides the answer.
// A cancel and a deadline share the same cmd.Cancel kill; cliversion's tests
// cover the deadline itself.
func TestDetectVersionCancelKillsProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "escaped")
	fifo := filepath.Join(t.TempDir(), "exited")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	_, p := versionFake(t, "pi", `(
exec 3>"$FIFO"
/bin/sleep 60
printf escaped > "$MARKER"
) &
wait`)
	p.Env["MARKER"], p.Env["FIFO"] = marker, fifo
	// Opening the read end unblocks the descendant's own open, so a reader
	// proves the descendant is running before the test ends the run.
	readers := make(chan *os.File, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			readers <- nil
			return
		}
		readers <- f
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan VersionInfo, 1)
	start := time.Now()
	go func() { result <- detectVersion(ctx, p, time.Minute) }()
	var reader *os.File
	select {
	case reader = <-readers:
	case <-time.After(10 * time.Second):
		t.Fatal("the descendant never held its exit pipe")
	}
	if reader == nil {
		t.Fatal("the descendant never held its exit pipe")
	}
	defer reader.Close()
	cancel()
	var got VersionInfo
	select {
	case got = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("version detection did not stop")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the group kill took %s", elapsed)
	}
	if got.Error == "" || got.Version != "" || !got.Installed {
		t.Fatalf("timeout result: %+v", got)
	}
	// The pipe reaches EOF only when every writer closed: the descendant is
	// gone. A survivor holds it open and blocks here instead.
	closed := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); closed <- err }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("reading the descendant's exit pipe: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("descendant survived the process-group kill")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant survived: %v", err)
	}
}

func TestDetectVersionBoundsOutputAndHidesErrors(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"stderr", "printf 'pi 3.4.5-beta.2\\n' >&2", "3.4.5-beta.2"},
		{"failure", "printf '%s' \"$SECRET\" >&2; exit 1", ""},
		{"large", "i=0; while [ $i -lt 10000 ]; do printf x; i=$((i+1)); done", ""},
		{"large-version", "printf 1.2.3-; i=0; while [ $i -lt 500 ]; do printf x; i=$((i+1)); done", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p := versionFake(t, "pi", tc.body)
			p.Env["SECRET"] = "PRIVATE_TOKEN_ABC"
			got := detectVersion(context.Background(), p, time.Minute)
			if got.Version != tc.want || len(got.Version) > 128 {
				t.Fatalf("version result: %+v", got)
			}
			if tc.want == "" && got.Error == "" {
				t.Fatal("expected fixed error")
			}
			if strings.Contains(got.Error, p.Env["SECRET"]) {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestDetectVersionCallerCancellationDoesNotPoisonCache(t *testing.T) {
	_, p := versionFake(t, "pi", "printf '1.2.3\\n'")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := DetectVersion(ctx, p); got.Error == "" {
		t.Fatalf("canceled result: %+v", got)
	}
	if got := detectVersion(context.Background(), p, time.Minute); got.Version != "1.2.3" {
		t.Fatalf("cancellation cached: %+v", got)
	}
}

// A version answer like "v2.1.2 (build)" must keep its major: no word
// boundary exists between the v and the 2, so a \b-anchored pattern matches
// "1.2" inside it and the health page shows a wrong version. Tokens glued
// to letters are not versions at all, and Dyna versions have exactly three
// number groups.
func TestDetectVersionParsesCompleteTokens(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"v-prefix", "printf 'v2.1.2 (build)\\n'", "2.1.2"},
		{"v-only", "printf 'v1.1.0'", "1.1.0"},
		{"prerelease", "printf 'pi 1.4.10-R6427.1\\n'", "1.4.10-R6427.1"},
		{"build", "printf 'x 2.1.2+build.4\\n'", "2.1.2+build.4"},
		{"glued", "printf 'xyz1.2.3\\n'", ""},
		{"four-part", "printf '1.2.3.4\\n'", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p := versionFake(t, "pi", tc.body)
			got := detectVersion(context.Background(), p, time.Minute)
			if got.Version != tc.want {
				t.Fatalf("version = %q, want %q (%+v)", got.Version, tc.want, got)
			}
			if tc.want == "" && got.Error == "" {
				t.Fatal("expected an error for an unrecognized version")
			}
		})
	}
}
