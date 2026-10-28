package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"dyna-agent/internal/fsutil"
)

// fixtureInstall creates npm's global layout without invoking npm or a provider.
func fixtureInstall(ctx context.Context, args, env []string) error {
	if args[0] == "curl" {
		source, err := url.Parse(args[len(args)-1])
		if err != nil {
			return err
		}
		cut := strings.LastIndex(source.Path, "/")
		pkg, version := strings.TrimPrefix(source.Path[:cut], "/"), source.Path[cut+1:]
		for i, arg := range args {
			if arg == "--output" {
				return os.WriteFile(args[i+1], []byte(recommendedMetadata(pkg, version)), 0600)
			}
		}
		return errors.New("missing metadata output")
	}

	root := ""
	for i, arg := range args {
		if arg == "--prefix" {
			root = args[i+1]
		}
	}
	spec := args[len(args)-1]
	h := "pi"
	if strings.HasPrefix(spec, "@anthropic-ai/") {
		h = "claude-code"
	}
	pkg, name := bundlePackage(h)
	version := strings.TrimPrefix(spec, pkg+"@")
	dynaFork := 0
	if spec != "" && spec == os.Getenv(piForkTarballEnv) {
		version = piForkVersion
		dynaFork = 1
	}
	dir := filepath.Join(root, "lib/node_modules", pkg)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
		return err
	}
	if err := fsutil.WriteExecutable(filepath.Join(dir, "cli.js"), []byte("#!/bin/sh\necho "+version+"\n"), 0700); err != nil {
		return err
	}
	if err := os.Symlink(filepath.Join("..", "lib/node_modules", pkg, "cli.js"), filepath.Join(root, "bin", name)); err != nil {
		return err
	}
	metadata, _ := json.Marshal(struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		DynaFork int    `json:"dynaFork,omitempty"`
	}{pkg, version, dynaFork})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), metadata, 0600); err != nil {
		return err
	}
	lock, _ := json.Marshal(map[string]any{"packages": map[string]any{"node_modules/" + pkg: map[string]string{"version": version, "integrity": "sha512-Zml4dHVyZQ=="}, "node_modules/other": map[string]string{"version": version, "integrity": "sha512-d3Jvbmc="}}})
	return os.WriteFile(filepath.Join(root, "package-lock.json"), lock, 0600)
}
func manager(t *testing.T) *BundleManager {
	t.Helper()
	tarball := filepath.Join(t.TempDir(), "pi-fork.tgz")
	if err := os.WriteFile(tarball, []byte("Pi fork fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(piForkTarballEnv, tarball)
	m := &BundleManager{Root: filepath.Join(t.TempDir(), "harnesses"), Run: fixtureInstall}
	t.Cleanup(func() { _ = removeBundle(m.Root) })
	// A shared first-use install keeps writing after a caller stops waiting, so
	// wait for it before this test's temp tree is removed (D-143).
	t.Cleanup(func() { _ = WaitInstalls(context.Background()) })
	return m
}
func install(t *testing.T, m *BundleManager, h, v string) Bundle {
	t.Helper()
	b, err := m.Install(context.Background(), h, v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestBundleInstallResolveAndSelection(t *testing.T) {
	m := manager(t)
	for _, h := range []string{"claude", "pi"} {
		old := install(t, m, h, "1.3.0")
		latest := install(t, m, h, "1.10.0")
		got, err := m.Resolve(h, "")
		if err != nil || got != latest {
			t.Fatalf("latest: %+v %v", got, err)
		}
		if err := m.Select(h, old.Version); err != nil {
			t.Fatal(err)
		}
		got, err = m.Resolve(h, "")
		if err != nil || got != old {
			t.Fatalf("selected: %+v %v", got, err)
		}
		if old.Harness == "claude" {
			t.Fatal("alias not canonicalized")
		}
		data, err := os.ReadFile(filepath.Join(latest.Directory, "bundle.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest bundleManifest
		if json.Unmarshal(data, &manifest) != nil {
			t.Fatal("invalid manifest")
		}
		if manifest.Integrity != "sha512-Zml4dHVyZQ==" || len(manifest.SHA256) != 64 || manifest.Platform == "" {
			t.Fatalf("metadata: %+v", manifest)
		}
		info, _ := os.Stat(latest.Directory)
		if info.Mode().Perm()&0222 != 0 {
			t.Fatal("bundle is writable")
		}
	}
	bundles, err := m.List()
	if err != nil || len(bundles) != 4 {
		t.Fatalf("list: %d %v", len(bundles), err)
	}
	m.Run = func(context.Context, []string, []string) error { t.Fatal("reinstalled completed bundle"); return nil }
	install(t, m, "pi", "1.10.0")
	if _, err := m.Resolve("pi", "9.0.0"); err == nil {
		t.Fatal("missing pin resolved")
	}
}
func TestInstallPrivateEnvironment(t *testing.T) {
	t.Setenv("npm_config_registry", "https://secret:password@invalid")
	t.Setenv("NPM_TOKEN", "fixture-token")
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		if strings.Join(args[:3], " ") != "npm install --global" {
			t.Fatalf("args: %v", args)
		}
		values := map[string]string{}
		for _, item := range env {
			key, value, _ := strings.Cut(item, "=")
			values[key] = value
		}
		for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
			if values[key] == os.Getenv(key) || !inside(m.Root, values[key]) {
				t.Fatalf("installer root %s is not private: %s", key, values[key])
			}
		}
		if values["NPM_TOKEN"] != "" || values["npm_config_registry"] != "" {
			t.Fatal("inherited npm credentials")
		}
		for _, key := range []string{"npm_config_cache", "npm_config_userconfig", "npm_config_globalconfig"} {
			if !inside(m.Root, values[key]) {
				t.Fatalf("non-private %s", key)
			}
		}
		return fixtureInstall(ctx, args, env)
	}
	install(t, m, "pi", "1.3.0+fixture")
}
func TestRejectInvalidHarnessAndVersion(t *testing.T) {
	m := manager(t)
	m.Run = func(context.Context, []string, []string) error { t.Fatal("installer called"); return nil }
	for _, v := range []string{"", "latest", "^1.0.0", "1", "v1.2.3", "01.2.3", "1.2.3-01", "../accounts", "1.2.3;echo token", "1.2.3\n", "1.2.3/../../accounts", "https://user:secret@host"} {
		if _, err := m.Install(context.Background(), "pi", v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, h := range []string{"unknown", "../accounts", "https://user:secret@host"} {
		_, err := m.Install(context.Background(), h, "1.0.0")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsupported error: %v", err)
		}
	}
}
func TestInstallFailureDoesNotPublishOrLeak(t *testing.T) {
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		if err := fixtureInstall(ctx, args, env); err != nil {
			return err
		}
		return errors.New("https://user:fixture-secret@registry")
	}
	_, err := m.Install(context.Background(), "pi", "1.3.0")
	if err == nil || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	bundles, err := m.List()
	if err != nil || len(bundles) != 0 {
		t.Fatalf("partial bundles: %v %v", bundles, err)
	}
	entries, _ := os.ReadDir(filepath.Join(m.Root, "pi"))
	if len(entries) != 0 {
		t.Fatal("staging retained")
	}
}
func TestRejectEscapingPathsAndTampering(t *testing.T) {
	for _, attack := range []string{"executable", "other-link", "absolute-inside", "manifest-path", "modified-hash", "harness-directory", "root", "lock"} {
		t.Run(attack, func(t *testing.T) {
			m := manager(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "credential")
			if err := os.WriteFile(sentinel, []byte("fixture-private"), 0700); err != nil {
				t.Fatal(err)
			}
			if attack == "root" {
				if err := os.Symlink(outside, m.Root); err != nil {
					t.Fatal(err)
				}
			}
			if attack == "harness-directory" || attack == "lock" {
				if err := os.MkdirAll(m.Root, 0700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(m.Root, "pi")
				if attack == "lock" {
					link = filepath.Join(m.Root, ".install.lock")
				}
				target := outside
				if attack == "lock" {
					target = sentinel
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			}
			m.Run = func(ctx context.Context, args, env []string) error {
				if err := fixtureInstall(ctx, args, env); err != nil {
					return err
				}
				if attack == "executable" {
					_ = os.Remove(filepath.Join(args[4], "bin/pi"))
					return os.Symlink(sentinel, filepath.Join(args[4], "bin/pi"))
				}
				if attack == "other-link" {
					return os.Symlink(outside, filepath.Join(args[4], "escape"))
				}
				if attack == "absolute-inside" {
					return os.Symlink(filepath.Join(args[4], "package-lock.json"), filepath.Join(args[4], "absolute-link"))
				}
				return nil
			}
			b, err := m.Install(context.Background(), "pi", "1.3.0")
			if attack == "manifest-path" || attack == "modified-hash" {
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(b.Directory, 0700); err != nil {
					t.Fatal(err)
				}
				if attack == "manifest-path" {
					path := filepath.Join(b.Directory, "bundle.json")
					_ = os.Chmod(path, 0600)
					data, _ := os.ReadFile(path)
					var manifest bundleManifest
					_ = json.Unmarshal(data, &manifest)
					manifest.Executable = sentinel
					data, _ = json.Marshal(manifest)
					_ = os.WriteFile(path, data, 0600)
				} else {
					path, _ := filepath.EvalSymlinks(b.Executable)
					_ = os.Chmod(path, 0700)
					_ = os.WriteFile(path, []byte("changed"), 0700)
				}
				_, err = m.Resolve("pi", "1.3.0")
			}
			if err == nil {
				t.Fatal("unsafe bundle accepted")
			}
			data, readErr := os.ReadFile(sentinel)
			if readErr != nil || string(data) != "fixture-private" {
				t.Fatal("outside file changed")
			}
		})
	}
}
func TestConcurrentInstallsSerializeAndReadersSeeCompleteBundles(t *testing.T) {
	m := manager(t)
	var active, peak, calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	m.Run = func(ctx context.Context, args, env []string) error {
		n := active.Add(1)
		defer active.Add(-1)
		if n > peak.Load() {
			peak.Store(n)
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return fixtureInstall(ctx, args, env)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(1)
	go func() { defer wg.Done(); _, err := m.Install(context.Background(), "pi", "1.3.0"); errs <- err }()
	<-entered
	wg.Add(2)
	go func() { defer wg.Done(); _, err := m.Install(context.Background(), "pi", "1.3.0"); errs <- err }()
	go func() {
		defer wg.Done()
		bundles, err := m.List()
		if err == nil && len(bundles) > 1 {
			err = errors.New("partial bundle visible")
		}
		errs <- err
	}()
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("peak %d calls %d", peak.Load(), calls.Load())
	}
}
func TestInstallLockCancellation(t *testing.T) {
	m := manager(t)
	unlock, err := m.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.Install(ctx, "pi", "1.3.0"); err == nil {
		t.Fatal("lock wait ignored cancellation")
	}
}
func TestGCKeepsNewestAndExactPinsAndNeverAccounts(t *testing.T) {
	m := manager(t)
	accountRoot := filepath.Join(filepath.Dir(m.Root), "accounts")
	if err := os.MkdirAll(accountRoot, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(accountRoot, "credential")
	if err := os.WriteFile(sentinel, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var pinned []PinnedBundle
	for _, h := range []string{"claude-code", "pi"} {
		for _, v := range []string{"1.3.0", "2.0.0", "3.0.0", "4.0.0", "5.0.0"} {
			b := install(t, m, h, v)
			if v == "1.3.0" {
				pinned = append(pinned, PinnedBundle{Bundle: b})
			}
		}
	}
	// A malicious caller's directory is never used as a deletion target.
	pinned = append(pinned, PinnedBundle{Bundle: Bundle{Harness: "pi", Version: "2.0.0", Directory: accountRoot}})
	if err := m.Select("claude", "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := m.GC(pinned); err != nil {
		t.Fatal(err)
	}
	bundles, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 8 {
		t.Fatalf("kept %d bundles: %+v", len(bundles), bundles)
	}
	for _, b := range bundles {
		if b.Version == "3.0.0" {
			t.Fatal("old unpinned bundle retained")
		}
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "fixture" {
		t.Fatal("GC touched accounts")
	}
	// Unknown entries, even symlinks to accounts, stay outside GC's traversal.
	if err := os.Symlink(accountRoot, filepath.Join(m.Root, "accounts")); err != nil {
		t.Fatal(err)
	}
	if err := m.GC(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatal(err)
	}
}

func TestInstallOfOneHarnessDoesNotBlockAnother(t *testing.T) {
	m := manager(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	m.Run = func(ctx context.Context, args, env []string) error {
		if strings.HasPrefix(args[len(args)-1], "@anthropic-ai/") {
			close(entered)
			<-release
		}
		return fixtureInstall(ctx, args, env)
	}
	claude := make(chan error, 1)
	go func() { _, err := m.Install(context.Background(), "claude", "1.0.0"); claude <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := m.Install(ctx, "pi", "2.0.0"); err != nil {
		t.Fatalf("pi waited for the claude download: %v", err)
	}
	if _, err := m.LeaseManaged(ctx, Bundle{Harness: "pi", Version: "2.0.0"}); err != nil {
		t.Fatalf("lease waited for the claude download: %v", err)
	}
	close(release)
	if err := <-claude; err != nil {
		t.Fatal(err)
	}
}

func TestInstallsOfOneHarnessSerialize(t *testing.T) {
	m := manager(t)
	// The virtual clock lets the second installer retry its lock without
	// waiting on real time, and synctest.Wait reports when both installers
	// are blocked, so no timer decides the answer.
	synctest.Test(t, func(t *testing.T) {
		var active, peak atomic.Int32
		entered := make(chan struct{}, 2)
		release := make(chan struct{})
		m.Run = func(ctx context.Context, args, env []string) error {
			n := active.Add(1)
			defer active.Add(-1)
			if n > peak.Load() {
				peak.Store(n)
			}
			entered <- struct{}{}
			<-release
			return fixtureInstall(ctx, args, env)
		}
		var wg sync.WaitGroup
		for _, v := range []string{"1.3.0", "2.0.0"} {
			wg.Go(func() {
				if _, err := m.Install(context.Background(), "pi", v); err != nil {
					t.Error(err)
				}
			})
		}
		// One installer holds the harness lock inside Run; the other waits
		// for that lock, so it never enters Run at the same time.
		<-entered
		synctest.Wait()
		if peak.Load() != 1 {
			t.Fatalf("concurrent installers of one harness: %d", peak.Load())
		}
		close(release)
		wg.Wait()
	})
}

func TestIntegrityFallbackAndMissingMetadata(t *testing.T) {
	for _, withIntegrity := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "package-metadata"}[withIntegrity], func(t *testing.T) {
			m := manager(t)
			m.Run = func(ctx context.Context, args, env []string) error {
				if args[0] == "curl" {
					return fixtureInstall(ctx, args, env)
				}
				if err := fixtureInstall(ctx, args, env); err != nil {
					return err
				}
				if err := os.Remove(filepath.Join(args[4], "package-lock.json")); err != nil {
					return err
				}
				if withIntegrity {
					data := []byte(`{"name":"` + piNPMPackage + `","version":"1.3.0","_integrity":"sha512-Zml4dHVyZQ=="}`)
					return os.WriteFile(filepath.Join(args[4], "lib/node_modules", piNPMPackage, "package.json"), data, 0600)
				}
				return nil
			}
			b := install(t, m, "pi", "1.3.0")
			data, err := os.ReadFile(filepath.Join(b.Directory, "bundle.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest bundleManifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if withIntegrity && manifest.Integrity != "sha512-Zml4dHVyZQ==" {
				t.Fatal("lost exact package integrity")
			}
			if !withIntegrity && !recommendedIntegrity(manifest.Integrity) {
				t.Fatal("missing exact registry integrity")
			}
		})
	}
}

func TestGCRejectsHarnessLinkToAccounts(t *testing.T) {
	m := manager(t)
	accounts := t.TempDir()
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(accounts, filepath.Join(m.Root, "pi")); err != nil {
		t.Fatal(err)
	}
	if err := m.GC(nil); err == nil {
		t.Fatal("GC followed harness symlink")
	}
	if entries, err := os.ReadDir(accounts); err != nil || len(entries) != 0 {
		t.Fatal("GC changed accounts")
	}
}

func TestNewBundleManagerUsesDataRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	if got := NewBundleManager().Root; got != filepath.Join(root, "dyna/harnesses") {
		t.Fatalf("root: %s", got)
	}
}

func TestSelectionNeverReadsOutsideBundleRoot(t *testing.T) {
	m := manager(t)
	install(t, m, "pi", "1.3.0")
	latest := install(t, m, "pi", "2.0.0")
	outside := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(outside, []byte("1.3.0"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(m.Root, ".pi-selected")); err != nil {
		t.Fatal(err)
	}
	got, err := m.Resolve("pi", "")
	if err != nil || got != latest {
		t.Fatalf("followed outside selection: %+v %v", got, err)
	}
	if err := m.GC(nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "1.3.0" {
		t.Fatal("outside selection changed")
	}
}

func TestClaudeBundleUsesNativeBinaryWhenScriptsAreBlocked(t *testing.T) {
	platform := map[string]string{"linux/amd64": "linux-x64", "linux/arm64": "linux-arm64", "darwin/amd64": "darwin-x64", "darwin/arm64": "darwin-arm64"}[runtime.GOOS+"/"+runtime.GOARCH]
	if platform == "" {
		t.Skip("no Claude Code native package for this platform")
	}
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		if err := fixtureInstall(ctx, args, env); err != nil {
			return err
		}
		// npm 12 leaves the stub in bin and installs the native optional dependency.
		dir := filepath.Join(args[4], "lib/node_modules/@anthropic-ai/claude-code/node_modules/@anthropic-ai/claude-code-"+platform)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "claude"), []byte("\x7fELF native"), 0700)
	}
	b := install(t, m, "claude", "2.0.0")
	if want := filepath.Join(b.Directory, "lib/node_modules/@anthropic-ai/claude-code/node_modules/@anthropic-ai/claude-code-"+platform, "claude"); b.Executable != want {
		t.Fatalf("executable = %s, want %s", b.Executable, want)
	}
	if again, err := m.Resolve("claude", "2.0.0"); err != nil || again.Executable != b.Executable {
		t.Fatalf("resolve = %#v, %v", again, err)
	}
}

// Resolve reads only the requested harness. Each read hashes the executable,
// so reading every harness made each lookup slow, and one damaged bundle
// stopped every harness.
func TestResolveReadsOnlyTheRequestedHarness(t *testing.T) {
	m := manager(t)
	pi := install(t, m, "pi", "1.3.0")
	claude := install(t, m, "claude", "1.0.0")
	manifest := filepath.Join(claude.Directory, "bundle.json")
	if err := os.Chmod(claude.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		t.Fatal("invalid manifest")
	}
	data["sha256"] = strings.Repeat("0", 64)
	raw, _ = json.Marshal(data)
	if err := os.Chmod(manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Resolve("pi", ""); err != nil || got != pi {
		t.Fatalf("pi = %+v, %v; a damaged claude bundle must not block it", got, err)
	}
	if _, err := m.Resolve("claude", ""); err == nil {
		t.Fatal("damaged claude bundle resolved")
	}
}

// The hash cache must not accept changed bytes when the size, mode and
// modification time are restored: the change time still moves.
func TestResolveRejectsModifiedExecutableWithRestoredMtime(t *testing.T) {
	m := manager(t)
	b := install(t, m, "pi", "1.3.0")
	if _, err := m.Resolve("pi", ""); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(b.Executable)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(b.Executable)
	if err != nil || len(data) == 0 {
		t.Fatal("cannot read executable", err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.Chmod(b.Executable, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(b.Executable, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(b.Executable, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve("pi", ""); err == nil {
		t.Fatal("modified executable accepted after restoring its modification time")
	}
}

func TestNPMInstallRejectsMismatchedRegistryReceipt(t *testing.T) {
	for _, body := range []string{
		recommendedMetadata("@example/other", "1.0.0"),
		recommendedMetadata(piNPMPackage, "1.1.1"),
		`{"name":"` + piNPMPackage + `","version":"1.3.0","dist":{"integrity":"sha512-bad"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			m := manager(t)
			m.Run = func(ctx context.Context, args, env []string) error {
				if args[0] == "curl" {
					for i, arg := range args {
						if arg == "--output" {
							return os.WriteFile(args[i+1], []byte(body), 0600)
						}
					}
					return errors.New("missing metadata output")
				}
				if err := fixtureInstall(ctx, args, env); err != nil {
					return err
				}
				return os.Remove(filepath.Join(args[4], "package-lock.json"))
			}
			if _, err := m.Install(context.Background(), "pi", "1.3.0"); err == nil {
				t.Fatal("published a bundle with an unverified registry receipt")
			}
			bundles, err := m.List()
			if err != nil || len(bundles) != 0 {
				t.Fatal("published mismatched package", bundles, err)
			}
		})
	}
}
