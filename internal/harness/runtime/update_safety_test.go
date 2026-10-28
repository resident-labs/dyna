package runtime

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
)

// scriptedManager installs bundles whose fixture executable runs script, so a
// test controls what the new version answers to its startup probe.
func scriptedManager(t *testing.T, script string) *BundleManager {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		if err := fixtureInstall(ctx, args, env); err != nil {
			return err
		}
		if args[0] == "npm" {
			rewriteFixtureExecutable(t, args[4], script)
		}
		return nil
	}
	return m
}

// rewriteFixtureExecutable replaces the installed cli.js stub with script.
func rewriteFixtureExecutable(t *testing.T, root, script string) {
	t.Helper()
	err := filepath.WalkDir(filepath.Join(root, "lib", "node_modules"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "cli.js" {
			return fsutil.WriteExecutable(path, []byte(script), 0700)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A release that cannot start must never become the default. The update path
// must prove the new version first; one bad release must not break every
// agent on that harness.
func TestInstallAndSelectRejectsNewVersionThatCannotStart(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\nexit 3\n")
	old := install(t, m, "pi", "1.3.0-dyna.4")
	if err := m.Select("pi", old.Version); err != nil {
		t.Fatal(err)
	}
	_, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4")
	if err == nil || !strings.Contains(err.Error(), "startup check") {
		t.Fatalf("broken update accepted: %v", err)
	}
	got, err := m.Resolve("pi", "")
	if err != nil || got.Version != old.Version {
		t.Fatalf("selection changed to a broken version: %+v %v", got, err)
	}
}

// The probe is a handshake, not a provider call: the new version must start
// and answer with a version. Silence counts as broken.
func TestInstallAndSelectRejectsNewVersionWithoutVersionAnswer(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\necho no-version-here\n")
	old := install(t, m, "pi", "1.3.0-dyna.4")
	if err := m.Select("pi", old.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4"); err == nil || !strings.Contains(err.Error(), "startup check") {
		t.Fatalf("silent update accepted: %v", err)
	}
	if got, err := m.Resolve("pi", ""); err != nil || got.Version != old.Version {
		t.Fatalf("selection changed: %+v %v", got, err)
	}
}

// The handshake answer must stay inside the harness's supported range: a
// binary that reports an incompatible build is rejected too.
func TestInstallAndSelectRejectsUnsupportedReportedVersion(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\necho 1.0.9\n")
	old := install(t, m, "pi", "1.3.0-dyna.4")
	if err := m.Select("pi", old.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "1.4.0-dyna.4"); err == nil || !strings.Contains(err.Error(), "startup check") {
		t.Fatalf("mismatched update accepted: %v", err)
	}
	if got, err := m.Resolve("pi", ""); err != nil || got.Version != old.Version {
		t.Fatalf("selection changed: %+v %v", got, err)
	}
}

// A version that never answers must not be selected. The shortened probe
// timeout keeps the test fast; production uses the default.
func TestProbeRejectsHangingVersion(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\nsleep 30\n")
	m.ProbeTimeout = 200 * time.Millisecond
	old := install(t, m, "pi", "1.3.0-dyna.4")
	if err := m.Select("pi", old.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4"); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("hanging update accepted: %v", err)
	}
	if got, err := m.Resolve("pi", ""); err != nil || got.Version != old.Version {
		t.Fatalf("selection changed: %+v %v", got, err)
	}
}

// A successful update records the replaced selection as the rollback target,
// keeps it on disk, and one rollback command restores it.
func TestRollbackRestoresLastGoodVersion(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\necho 2.0.0-dyna.4\n")
	first := install(t, m, "pi", "1.3.0-dyna.4")
	if err := m.Select("pi", first.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if got, err := m.LastGoodVersion("pi"); err != nil || got != first.Version {
		t.Fatalf("rollback target = %q, %v; want %q", got, err, first.Version)
	}
	// Reinstalling the selected version must not move the target.
	if _, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.LastGoodVersion("pi"); got != first.Version {
		t.Fatalf("rollback target moved to %q", got)
	}
	back, err := m.Rollback(context.Background(), "pi")
	if err != nil || back.Version != first.Version {
		t.Fatalf("rollback: %+v %v", back, err)
	}
	if got, err := m.Resolve("pi", ""); err != nil || got.Version != first.Version {
		t.Fatalf("selection after rollback: %+v %v", got, err)
	}
	if _, err := m.Rollback(context.Background(), "pi"); err == nil || !strings.Contains(err.Error(), "No earlier good version") {
		t.Fatalf("second rollback: %v", err)
	}
}

// Rolling back with no update on record has no target and changes nothing.
func TestRollbackWithoutUpdateFails(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\necho 1.3.0-dyna.4\n")
	if _, err := m.InstallAndSelect(context.Background(), "pi", "1.3.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rollback(context.Background(), "pi"); err == nil || !strings.Contains(err.Error(), "No earlier good version") {
		t.Fatalf("rollback without update: %v", err)
	}
	if got, err := m.Resolve("pi", ""); err != nil || got.Version != "1.3.0-dyna.4" {
		t.Fatalf("selection changed: %+v %v", got, err)
	}
}

// The last good version survives GC even when it is the oldest installed and
// sits outside the newest two.
func TestGCKeepsLastGoodVersion(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\necho 4.0.0-dyna.4\n")
	oldest := install(t, m, "pi", "1.3.0-dyna.4")
	install(t, m, "pi", "2.0.0-dyna.4")
	install(t, m, "pi", "3.0.0-dyna.4")
	install(t, m, "pi", "4.0.0-dyna.4")
	if err := m.Select("pi", oldest.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "4.0.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if err := m.GC(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve("pi", "2.0.0-dyna.4"); err == nil {
		t.Fatal("GC kept an unprotected version outside the newest two")
	}
	if _, err := m.Resolve("pi", oldest.Version); err != nil {
		t.Fatal("GC removed the rollback target:", err)
	}
	if _, err := m.Rollback(context.Background(), "pi"); err != nil {
		t.Fatal(err)
	}
}

// Remove must not delete the rollback target.
func TestRemoveKeepsLastGoodVersion(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\necho 2.0.0-dyna.4\n")
	old := install(t, m, "pi", "1.3.0-dyna.4")
	if err := m.Select("pi", old.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(context.Background(), "pi", old.Version, nil, true); err == nil || !strings.Contains(err.Error(), "rollback target") {
		t.Fatalf("rollback target removed: %v", err)
	}
}

// A first-use install leaves no selection marker: the newest installed
// bundle is the effective default. A failed update must not change that
// default, and the broken candidate must never become visible to Resolve,
// not even during the probe.
func TestFailedUpdateLeavesFirstUseDefaultAlone(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\nexit 3\n")
	if _, err := m.Install(context.Background(), "pi", "1.3.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4"); err == nil || !strings.Contains(err.Error(), "startup check") {
		t.Fatalf("broken update accepted: %v", err)
	}
	if got, err := m.Resolve("pi", ""); err != nil || got.Version != "1.3.0-dyna.4" {
		t.Fatalf("effective default changed: %+v %v", got, err)
	}
	if got, err := m.LastGoodVersion("pi"); err != nil || got != "" {
		t.Fatalf("failed update recorded a rollback target: %q %v", got, err)
	}
	if _, err := m.Resolve("pi", "2.0.0-dyna.4"); err == nil {
		t.Fatal("broken candidate stayed published")
	}
}

// An update from the marker-less first-use state still records the effective
// prior default as its rollback target.
func TestUpdateFromFirstUseStateRecordsRollbackTarget(t *testing.T) {
	m := scriptedManager(t, "#!/bin/sh\necho 2.0.0-dyna.4\n")
	if _, err := m.Install(context.Background(), "pi", "1.3.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	if got, err := m.LastGoodVersion("pi"); err != nil || got != "1.3.0-dyna.4" {
		t.Fatalf("rollback target = %q, %v; want the prior effective default 1.3.0", got, err)
	}
	back, err := m.Rollback(context.Background(), "pi")
	if err != nil || back.Version != "1.3.0-dyna.4" {
		t.Fatalf("rollback: %+v %v", back, err)
	}
}

// lockWaitCtx reports when its Done method is first asked. The bundle
// manager's contended lock wait asks Done exactly when a caller blocks on the
// lock, so a test can observe that moment and release what holds the lock.
type lockWaitCtx struct {
	context.Context
	once  sync.Once
	asked chan struct{}
}

func (c *lockWaitCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.asked) })
	return c.Context.Done()
}

// Two overlapping updates must each record the default they really replaced.
// The first update is held inside its download until the second blocks on the
// harness lock, which proves the second read the pre-update state already
// (old implementation) or waits for the lock before reading it (fixed one).
func TestConcurrentUpdatesRecordEachReplacedDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := manager(t)
	// 1.3.0 is only the implicit default: no selection marker exists. Seed it
	// before the notification wrapper, so the seed sends no notification.
	m.Run = fixtureInstall
	if _, err := m.Install(context.Background(), "pi", "1.3.0-dyna.4"); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	// A failed assertion must not leave the held update blocked forever.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	second := &lockWaitCtx{Context: ctx, asked: make(chan struct{})}
	m.Run = func(ctx context.Context, args, env []string) error {
		if err := fixtureInstall(ctx, args, env); err != nil {
			return err
		}
		if args[0] != "npm" {
			return nil
		}
		spec := args[len(args)-1]
		version := spec[strings.LastIndex(spec, "@")+1:]
		started <- version
		rewriteFixtureExecutable(t, args[4], "#!/bin/sh\necho "+version+"\n")
		// The first update stays inside its download until the test released
		// it, after the second update blocked on the harness lock it holds.
		if version == "2.0.0-dyna.4" {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := m.InstallAndSelect(ctx, "pi", "2.0.0-dyna.4")
		failures <- err
	}()
	if got := <-started; got != "2.0.0-dyna.4" {
		t.Fatalf("first update did not start: %q", got)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := m.InstallAndSelect(second, "pi", "3.0.0-dyna.4")
		failures <- err
	}()
	// The second update now blocks on the harness lock the first one holds.
	// In the old implementation it has already read the pre-update default;
	// in the fixed one it reads that default only after the lock is its turn.
	<-second.asked
	close(release)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, err := m.Resolve("pi", ""); err != nil || got.Version != "3.0.0-dyna.4" {
		t.Fatalf("final selection: %+v %v", got, err)
	}
	if got, err := m.LastGoodVersion("pi"); err != nil || got != "2.0.0-dyna.4" {
		t.Fatalf("rollback target = %q, %v; want 2.0.0, the default the second update replaced", got, err)
	}
	if back, err := m.Rollback(context.Background(), "pi"); err != nil || back.Version != "2.0.0-dyna.4" {
		t.Fatalf("rollback: %+v %v", back, err)
	}
}

// The probe leaves neither its scope nor its private HOME behind, on success
// and on failure.
func TestProbeCleansUpItsScope(t *testing.T) {
	for _, script := range []string{"#!/bin/sh\necho 2.0.0-dyna.4\n", "#!/bin/sh\nexit 3\n"} {
		m := scriptedManager(t, script)
		old := install(t, m, "pi", "1.3.0-dyna.4")
		if err := m.Select("pi", old.Version); err != nil {
			t.Fatal(err)
		}
		_, _ = m.InstallAndSelect(context.Background(), "pi", "2.0.0-dyna.4")
		entries, err := os.ReadDir(m.Root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".probe-") {
				t.Fatalf("probe left %s behind with script %q", e.Name(), script)
			}
		}
	}
}

// The probe runs in a throwaway private scope: the new version sees no
// personal roots, while owner toolchains stay available.
func TestProbeEnvironmentIsPrivate(t *testing.T) {
	t.Setenv("HOME", "/owner-home")
	t.Setenv("PATH", "/usr/bin:/bin")
	a, err := Adapter("pi")
	if err != nil {
		t.Fatal(err)
	}
	env, err := probeEnvironment(a, "/private-scope")
	if err != nil {
		t.Fatal(err)
	}
	has := func(pair string) bool {
		for _, item := range env {
			if item == pair {
				return true
			}
		}
		return false
	}
	for _, pair := range []string{
		"HOME=/private-scope.home",
		"XDG_CONFIG_HOME=/private-scope.home/.config",
		"XDG_DATA_HOME=/private-scope.home/.local/share",
		"PI_CODING_AGENT_DIR=/private-scope",
		"PI_CODING_AGENT_SESSION_DIR=/private-scope/sessions",
		"PATH=/usr/bin:/bin",
	} {
		if !has(pair) {
			t.Errorf("probe environment is missing %q", pair)
		}
	}
	if has("HOME=/owner-home") {
		t.Error("probe environment leaks the owner HOME")
	}
}
