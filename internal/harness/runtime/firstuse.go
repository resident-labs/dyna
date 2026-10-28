package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// FirstUse finds a harness CLI and installs it as a private bundle the first
// time it is missing. It is the only automatic install path, and it always
// installs through BundleManager.Install.
type FirstUse struct {
	Bundles *BundleManager
	// Latest returns the exact version to install. Nil uses RecommendedVersions.
	Latest func(context.Context, string) (string, error)
	// Progress, when set, runs once before a download starts.
	Progress func(harness, version string)
}

func NewFirstUse() *FirstUse { return &FirstUse{Bundles: NewBundleManager()} }

// EnsureBundle resolves a private bundle and installs it when it is missing.
func EnsureBundle(ctx context.Context, harness, version string) (Bundle, error) {
	b, _, err := NewFirstUse().EnsureBundle(ctx, harness, version)
	return b, err
}

// EnsureBundle returns true when this call installed the bundle. Calls for the
// same harness share one install, also across processes. Calls for other
// harnesses do not wait for it.
func (f *FirstUse) EnsureBundle(ctx context.Context, harness, version string) (Bundle, bool, error) {
	a, err := Adapter(harness)
	if err != nil {
		return Bundle{}, false, err
	}
	if b, err := f.Bundles.Resolve(a.Harness, version); err == nil {
		return b, false, nil
	}
	key := f.Bundles.Root + "\x00" + a.Harness + "\x00" + version
	firstUseMu.Lock()
	call := firstUseCalls[key]
	if call == nil {
		call = &firstUseCall{done: make(chan struct{})}
		firstUseCalls[key] = call
		go func() {
			// One shared install outlives a canceled caller. The bundle
			// installer bounds its own work.
			ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			call.bundle, call.installed, call.err = f.install(ictx, a, version)
			firstUseMu.Lock()
			delete(firstUseCalls, key)
			firstUseMu.Unlock()
			close(call.done)
		}()
	}
	firstUseMu.Unlock()
	select {
	case <-call.done:
		return call.bundle, call.installed, call.err
	case <-ctx.Done():
		return Bundle{}, false, fmt.Errorf("%s install canceled", a.Binary)
	}
}

type firstUseCall struct {
	done      chan struct{}
	bundle    Bundle
	installed bool
	err       error
}

// WaitInstalls waits until no shared first-use install is running. An install
// runs in the background so one shared install outlives a caller that stops
// waiting, and it keeps writing into the bundle root until it finishes. A test
// or a shutdown that removes that root waits here first (D-143).
func WaitInstalls(ctx context.Context) error {
	for {
		firstUseMu.Lock()
		pending := make([]chan struct{}, 0, len(firstUseCalls))
		for _, call := range firstUseCalls {
			pending = append(pending, call.done)
		}
		firstUseMu.Unlock()
		if len(pending) == 0 {
			return nil
		}
		for _, done := range pending {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

var (
	firstUseMu    sync.Mutex
	firstUseCalls = map[string]*firstUseCall{}
)

func (f *FirstUse) install(ctx context.Context, a RuntimeAdapter, version string) (Bundle, bool, error) {
	unlock, err := f.lock(ctx, a.Harness)
	if err != nil {
		return Bundle{}, false, err
	}
	defer unlock()
	// Another process may have finished the install while this one waited.
	if b, err := f.Bundles.Resolve(a.Harness, version); err == nil {
		return b, false, nil
	}
	if version == "" {
		if version, err = f.LatestVersion(ctx, a.Harness); err != nil {
			return Bundle{}, false, err
		}
	}
	if f.Progress != nil {
		f.Progress(a.Harness, version)
	}
	b, err := f.Bundles.Install(ctx, a.Harness, version)
	if err != nil {
		return Bundle{}, false, fmt.Errorf("cannot install %s %s: %w. Check the internet connection and try again", a.Binary, version, err)
	}
	return b, true, nil
}

// LatestVersion returns the exact version Dyna installs when the caller names
// none.
func (f *FirstUse) LatestVersion(ctx context.Context, harness string) (string, error) {
	a, err := Adapter(harness)
	if err != nil {
		return "", err
	}
	latest := f.Latest
	if latest == nil {
		latest = recommendedInstallVersion
	}
	version, err := latest(ctx, a.Harness)
	if err != nil {
		return "", fmt.Errorf("cannot install %s: Dyna could not get a version to install (%s). Connect to the internet and try again", a.Binary, strings.TrimSuffix(err.Error(), "."))
	}
	return version, nil
}

func recommendedInstallVersion(ctx context.Context, harness string) (string, error) {
	for _, v := range RecommendedVersions(ctx) {
		if v.Harness != harness {
			continue
		}
		if v.Version != "" {
			return v.Version, nil
		}
		if v.Reason != "" {
			return "", errors.New(v.Reason)
		}
	}
	return "", errors.New("Dyna could not find a version to install")
}

func (f *FirstUse) lockPath(harness string) string {
	return filepath.Join(f.Bundles.Root, ".firstuse-"+harness+".lock")
}

// lock is per harness, so one first-use install never blocks another harness.
func (f *FirstUse) lock(ctx context.Context, harness string) (func(), error) {
	return f.Bundles.lockFile(ctx, filepath.Base(f.lockPath(harness)))
}

// Installing reports whether any process is installing this harness now.
func (f *FirstUse) Installing(harness string) bool {
	a, err := Adapter(harness)
	if err != nil {
		return false
	}
	fd, err := syscall.Open(f.lockPath(a.Harness), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	if err := syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
	}
	_ = syscall.Flock(fd, syscall.LOCK_UN)
	return false
}
