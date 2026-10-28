package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func firstUse(t *testing.T, latest string) (*FirstUse, *atomic.Int32) {
	t.Helper()
	installs := &atomic.Int32{}
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		installs.Add(1)
		return fixtureInstall(ctx, args, env)
	}
	return &FirstUse{Bundles: m, Latest: func(context.Context, string) (string, error) { return latest, nil }}, installs
}

func TestFirstUseInstallsMissingCLIOnce(t *testing.T) {
	f, installs := firstUse(t, "1.3.0")
	b, installed, err := f.EnsureBundle(context.Background(), "pi", "")
	if err != nil {
		t.Fatal(err)
	}
	if !installed || b.Version != "1.3.0" || b.Executable == "" {
		t.Fatalf("bundle = %#v, installed = %v", b, installed)
	}
	again, installed, err := f.EnsureBundle(context.Background(), "pi", "")
	if err != nil {
		t.Fatal(err)
	}
	if installed || again.Executable != b.Executable || installs.Load() != 1 {
		t.Fatalf("second call = %#v, installs = %d", again, installs.Load())
	}
}

func TestFirstUseConcurrentRequestsShareOneInstall(t *testing.T) {
	f, installs := firstUse(t, "1.3.0")
	release := make(chan struct{})
	// Run starts once the installer holds the per-harness lock, so the
	// channel reports an install that Installing must see as running.
	downloading := make(chan struct{}, 1)
	run := f.Bundles.Run
	f.Bundles.Run = func(ctx context.Context, args, env []string) error {
		downloading <- struct{}{}
		<-release
		return run(ctx, args, env)
	}
	var wg sync.WaitGroup
	results := make([]Bundle, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, errs[i] = f.EnsureBundle(context.Background(), "pi", "")
		}(i)
	}
	// Two more callers model separate processes: they skip the in-process
	// share and meet only at the per-harness file lock.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, _ := Adapter("pi")
			if _, _, err := f.install(context.Background(), a, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	deadline := time.After(time.Second)
	select {
	case <-downloading:
	case <-deadline:
		t.Fatal("no install reached the download")
	}
	if !f.Installing("pi") || f.Installing("claude-code") {
		t.Fatal("Installing does not report the running pi install")
	}
	close(release)
	wg.Wait()
	for i, err := range errs {
		if err != nil || results[i].Executable != results[0].Executable {
			t.Fatalf("caller %d: %#v %v", i, results[i], err)
		}
	}
	if installs.Load() != 1 || f.Installing("pi") {
		t.Fatalf("installs = %d, installing = %v", installs.Load(), f.Installing("pi"))
	}
}

func TestFirstUseOfflineGivesClearError(t *testing.T) {
	f, _ := firstUse(t, "")
	f.Latest = func(context.Context, string) (string, error) {
		return "", errors.New("Package metadata verification is unavailable.")
	}
	_, _, err := f.EnsureBundle(context.Background(), "claude", "")
	if err == nil || !strings.Contains(err.Error(), "cannot install claude") || !strings.Contains(err.Error(), "Connect to the internet") || strings.Contains(err.Error(), "PATH") {
		t.Fatalf("err = %v", err)
	}
	f, _ = firstUse(t, "1.3.0")
	f.Bundles.Run = func(context.Context, []string, []string) error { return errors.New("ENOTFOUND registry.npmjs.org") }
	_, _, err = f.EnsureBundle(context.Background(), "pi", "")
	if err == nil || !strings.Contains(err.Error(), "cannot install pi 1.3.0") || !strings.Contains(err.Error(), "Check the internet connection") || strings.Contains(err.Error(), "ENOTFOUND") {
		t.Fatalf("err = %v", err)
	}
	// The failure is not cached: the next call tries again.
	f.Bundles.Run = fixtureInstall
	if _, _, err := f.EnsureBundle(context.Background(), "pi", ""); err != nil {
		t.Fatal(err)
	}
}

func TestFirstUseLatestVersion(t *testing.T) {
	f, installs := firstUse(t, "1.3.0")
	if v, err := f.LatestVersion(context.Background(), "claude"); err != nil || v != "1.3.0" || installs.Load() != 0 {
		t.Fatalf("got %q, %v, %d installs", v, err, installs.Load())
	}
	f.Latest = func(context.Context, string) (string, error) {
		return "", errors.New("Version metadata is unavailable.")
	}
	if _, err := f.LatestVersion(context.Background(), "pi"); err == nil || !strings.Contains(err.Error(), "cannot install pi") || !strings.Contains(err.Error(), "Connect to the internet") {
		t.Fatalf("err = %v", err)
	}
}
