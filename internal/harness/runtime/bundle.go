// Package runtime manages Dyna's private, pinned harness bundles.
package runtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/store"
	"golang.org/x/mod/semver"
)

// PinnedBundle is one bundle held in use and what holds it: a profile
// selection, an open session, or the process of a live lease. Removal
// refusals name the holder, so the owner knows what to close first.
type PinnedBundle struct {
	Bundle
	Holder string
}

// BundleManager installs bundles with temporary private HOME and XDG roots. Run receives
// an argument vector and the complete environment; tests can replace it offline.
type BundleManager struct {
	Root string
	Run  func(context.Context, []string, []string) error
	// ProbeTimeout bounds one startup probe. Zero uses the default. Tests
	// shorten it to keep a hanging version fast.
	ProbeTimeout time.Duration
}

type bundleManifest struct {
	Harness    string `json:"harness"`
	Version    string `json:"version"`
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
	Package    string `json:"package"`
	Integrity  string `json:"integrity,omitempty"`
	Platform   string `json:"platform"`
}

func NewBundleManager() *BundleManager {
	return &BundleManager{Root: filepath.Join(runtimeDataDir(), "harnesses")}
}

// CanonicalHarness accepts the supported profile harness names and the Claude alias.
func CanonicalHarness(harness string) (string, error) {
	a, err := Adapter(harness)
	if err != nil {
		return "", err
	}
	return a.Harness, nil
}

// bundleHarness is CanonicalHarness for bundle operations, which also accept tools.
func bundleHarness(harness string) (string, error) {
	a, err := Adapter(harness)
	return a.Harness, err
}

var exactVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

var exactDateVersion = regexp.MustCompile(`^[0-9]{4}\.[0-9]{2}\.[0-9]{2}-[A-Za-z0-9]+$`)

func validVersion(v string) bool {
	return exactVersion.MatchString(v) && semver.IsValid("v"+v) || exactDateVersion.MatchString(v)
}
func bundlePackage(h string) (string, string) {
	a, err := Adapter(h)
	if err != nil {
		return "", ""
	}
	return a.NPMPackage, a.Binary
}
func failure() error { return errors.New("private bundle operation failed") }

// checkedDir rejects symlink components, including the configured root. Bundle
// operations must never follow a link into the accounts tree or personal files.
func checkedDir(path string, create bool) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return failure()
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(absolute, current), current) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && create {
			if err = os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
				return failure()
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return failure()
		}
	}
	return nil
}

// lock guards published bundles, selections and leases. Hold it only briefly.
func (m *BundleManager) lock(ctx context.Context) (func(), error) {
	return m.lockFile(ctx, ".install.lock")
}

// lockFile takes an exclusive flock on a file under Root.
func (m *BundleManager) lockFile(ctx context.Context, name string) (func(), error) {
	if ctx.Err() != nil {
		return nil, errors.New("private bundle operation canceled")
	}
	if m.Root == "" {
		return nil, failure()
	}
	if err := checkedDir(m.Root, true); err != nil {
		return nil, err
	}
	// O_NOFOLLOW prevents replacement of the lock with a credential-file link.
	fd, err := syscall.Open(filepath.Join(m.Root, name), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, failure()
	}
	f := os.NewFile(uintptr(fd), "bundle-lock")
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = f.Close()
			return nil, failure()
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, errors.New("private bundle operation canceled")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func validateTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return failure()
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			// Absolute staging links would break after publication.
			if err != nil || filepath.IsAbs(link) {
				return failure()
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil || !inside(root, target) {
				return failure()
			}
		} else if !entry.IsDir() && !entry.Type().IsRegular() {
			return failure()
		}
		return nil
	})
}
func executableHash(root, executable string) (string, error) {
	if filepath.IsAbs(executable) || !inside(root, filepath.Join(root, executable)) {
		return "", failure()
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, executable))
	if err != nil || !inside(root, target) {
		return "", failure()
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", failure()
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", failure()
	}
	// Every read checks the hash. Reuse it while the path names the same file
	// with the same size, mtime and ctime. A content or mode change sets
	// ctime, which a process cannot restore. Without ctime, hash again.
	ctime, cacheable := store.ChangeTime(st)
	key := hashKey{target, uint64(st.Dev), uint64(st.Ino), info.Size(), info.ModTime().UnixNano(), ctime}
	if sum, ok := executableHashes.Load(key); ok && cacheable {
		return sum.(string), nil
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", failure()
	}
	sum := sha256.Sum256(data)
	if cacheable {
		executableHashes.Store(key, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(sum[:]), nil
}

type hashKey struct {
	path             string
	dev, ino         uint64
	size, mod, ctime int64
}

// executableHashes memoizes executableHash for this process.
var executableHashes sync.Map

// errBundleUnusable marks a published bundle that lacks a file its harness
// runs beside the executable, such as a companion server.
var errBundleUnusable = errors.New("bundle cannot be used")

// read returns a verified published bundle. A bundle whose version Dyna's
// verified recipes no longer support, or without the files its adapter
// requires, comes back with errBundleUnusable, so Resolve, Select, Rollback,
// Lease and install reuse all refuse it and name the reason. The manifest is
// read and validated first, so its errors propagate unchanged; the range
// refusal keeps the manifest data on the bundle, letting cleanup remove an
// old version and protect the leases and pins of sessions that hold it.
func (m *BundleManager) read(h, v string) (Bundle, error) {
	b, manifestErr := m.readManifest(h, v)
	if ok, reason := supportedVersionRange(h, v); !ok {
		unusable := fmt.Errorf("%w: %s %s is not supported: %s", errBundleUnusable, h, v, reason)
		if manifestErr == nil {
			return b, unusable
		}
		// An old version that is not installed cannot run either way; the
		// range reason names the real problem. A manifest that exists but
		// cannot be read propagates its error, so cleanup never sees an
		// empty bundle and skips a directory it should have removed.
		if _, statErr := os.Lstat(filepath.Join(m.Root, h, v)); os.IsNotExist(statErr) {
			return Bundle{}, unusable
		}
		return Bundle{}, manifestErr
	}
	if manifestErr != nil {
		return Bundle{}, manifestErr
	}
	a, _ := Adapter(h)
	for _, name := range a.Requires {
		if info, err := os.Stat(filepath.Join(filepath.Dir(b.Executable), name)); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return b, fmt.Errorf("%w: %s %s has no %s beside its executable; install %s again", errBundleUnusable, h, v, name, h)
		}
	}
	return b, nil
}

func (m *BundleManager) readManifest(h, v string) (Bundle, error) {
	dir, err := filepath.Abs(filepath.Join(m.Root, h, v))
	if err != nil {
		return Bundle{}, failure()
	}
	if err := checkedDir(dir, false); err != nil {
		return Bundle{}, err
	}
	if err := validateTree(dir); err != nil {
		return Bundle{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "bundle.json"))
	if err != nil {
		return Bundle{}, failure()
	}
	var manifest bundleManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.Harness != h || manifest.Version != v {
		return Bundle{}, failure()
	}
	if filepath.IsAbs(manifest.Executable) || filepath.Clean(manifest.Executable) != manifest.Executable || strings.HasPrefix(manifest.Executable, ".."+string(filepath.Separator)) || manifest.Executable == "." {
		return Bundle{}, failure()
	}
	hash, err := executableHash(dir, manifest.Executable)
	if err != nil || hash != manifest.SHA256 {
		return Bundle{}, failure()
	}
	return Bundle{h, v, filepath.Join(dir, manifest.Executable), dir}, nil
}

// Install publishes a complete bundle by atomic rename. Existing bundles are
// verified and reused. Installer output and raw errors never reach callers.
func (m *BundleManager) Install(ctx context.Context, harness, version string) (Bundle, error) {
	return m.installCandidate(ctx, harness, version, nil, nil)
}

// installCandidate installs one bundle. When probe is set, a fresh candidate
// runs it in staging before publication, so a version that cannot start is
// never visible to Resolve; a reused published bundle is probed in place.
// When commit is set, it publishes and selects while the manager's lock is
// held, so the capture of the replaced default, the rename and the selection
// stay one critical section. Without commit, the candidate is only
// published.
func (m *BundleManager) installCandidate(ctx context.Context, harness, version string, probe func(context.Context, Bundle) error, commit func(h, version, staging, final string) (Bundle, error)) (Bundle, error) {
	h, err := bundleHarness(harness)
	if err != nil {
		return Bundle{}, err
	}
	// An unstamped binary names only the fork level; the tarball it installs
	// carries the exact build. Resolve the build now, so the bundle publishes
	// and resolves under the version its manifest really has. A source build
	// made by an older self-update stamps the local tarball but not its build
	// (D-417), so read the build from that tarball too.
	if a, aerr := Adapter(h); aerr == nil && a.distribution == distributionFork && version == piForkVersion && piForkBuild == "" {
		tarball := os.Getenv(piForkTarballEnv)
		if tarball == "" && piForkURL == "" {
			tarball = piForkAsset
		}
		if build, ok := forkTarballVersion(tarball); ok {
			version = build
		}
	}
	if !validVersion(version) {
		return Bundle{}, errors.New("private bundles require an exact version, not a moving tag or range")
	}
	if ok, reason := supportedVersionRange(h, version); !ok {
		return Bundle{}, errors.New(reason)
	}
	// The download holds only this harness's lock, so installs of other
	// harnesses, leases and cleanup do not wait for it.
	unlockHarness, err := m.lockFile(ctx, ".install-"+h+".lock")
	if err != nil {
		return Bundle{}, err
	}
	defer unlockHarness()
	parent := filepath.Join(m.Root, h)
	final := filepath.Join(parent, version)
	if b, done, err := m.published(ctx, h, version, parent, final); done {
		if err == nil && probe != nil {
			if perr := probe(ctx, b); perr != nil {
				return Bundle{}, perr
			}
		}
		if err == nil && commit != nil {
			unlock, lerr := m.lock(ctx)
			if lerr != nil {
				return Bundle{}, lerr
			}
			defer unlock()
			return commit(h, version, "", final)
		}
		return b, err
	}
	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return Bundle{}, failure()
	}
	staging, _ = filepath.Abs(staging)
	defer removeBundle(staging)
	private, err := os.MkdirTemp(m.Root, ".npm-")
	if err != nil {
		return Bundle{}, failure()
	}
	defer os.RemoveAll(private)
	config := filepath.Join(private, "npmrc")
	globalConfig := filepath.Join(private, "global-npmrc")
	if os.WriteFile(globalConfig, nil, 0600) != nil {
		return Bundle{}, failure()
	}
	if os.WriteFile(config, []byte("registry=https://registry.npmjs.org/\n"), 0600) != nil {
		return Bundle{}, failure()
	}
	env := make([]string, 0, len(os.Environ())+4)
	installScope := filepath.Join(private, "scope")
	if err := home.Ensure(installScope); err != nil {
		return Bundle{}, err
	}
	installEnv, err := home.ToolRoots(os.Environ())
	if err != nil {
		return Bundle{}, err
	}
	for key, value := range home.Roots(installScope) {
		installEnv[key] = value
	}
	a, _ := Adapter(h)
	if a.NativeEnv != nil {
		for key, value := range a.NativeEnv(installScope) {
			installEnv[key] = value
		}
	}
	for _, item := range NativeEnvironmentBase(h, os.Environ()) {
		key, _, _ := strings.Cut(item, "=")
		_, replaced := installEnv[key]
		if replaced || strings.HasPrefix(strings.ToLower(key), "npm_config_") || key == "NODE_AUTH_TOKEN" || key == "NPM_TOKEN" {
			continue
		}
		env = append(env, item)
	}
	for key, value := range installEnv {
		if !strings.HasPrefix(strings.ToLower(key), "npm_config_") {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "npm_config_cache="+filepath.Join(private, "cache"), "npm_config_userconfig="+config, "npm_config_globalconfig="+globalConfig, "npm_config_update_notifier=false")
	pkg, name := bundlePackage(h)
	executable := filepath.Join("bin", name)
	integrity := ""
	if a.Install != nil {
		artifact, err := a.Install(ctx, m, staging, version, env)
		if err != nil {
			return Bundle{}, err
		}
		executable, pkg, integrity = artifact.Executable, artifact.Package, artifact.Integrity
	} else {
		artifact, err := installNPM(ctx, m, staging, version, env, a)
		if err != nil {
			return Bundle{}, err
		}
		executable, pkg, integrity = artifact.Executable, artifact.Package, artifact.Integrity
	}
	if err := validateTree(staging); err != nil {
		return Bundle{}, err
	}
	if filepath.IsAbs(executable) || filepath.Clean(executable) != executable || strings.HasPrefix(executable, ".."+string(filepath.Separator)) {
		return Bundle{}, failure()
	}
	hash, err := executableHash(staging, executable)
	if err != nil {
		return Bundle{}, err
	}
	manifest := bundleManifest{h, version, executable, hash, pkg, integrity, runtime.GOOS + "/" + runtime.GOARCH}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	if os.WriteFile(filepath.Join(staging, "bundle.json"), data, 0600) != nil {
		return Bundle{}, failure()
	}
	if err := freezeBundle(staging); err != nil {
		return Bundle{}, err
	}
	if probe != nil {
		// The candidate is still in staging: a version that cannot start is
		// never published, so default resolution cannot see it, even briefly.
		if err := probe(ctx, Bundle{Harness: h, Version: version, Executable: filepath.Join(staging, executable), Directory: staging}); err != nil {
			return Bundle{}, err
		}
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return Bundle{}, err
	}
	defer unlock()
	if commit != nil {
		return commit(h, version, staging, final)
	}
	if os.Rename(staging, final) != nil {
		return Bundle{}, failure()
	}
	return m.read(h, version)
}

// InstallAndSelect publishes a verified bundle, proves the new version starts
// with one no-cost probe, and only then changes the default for new sessions.
// The probe runs before publication, so a failed candidate never becomes
// visible to Resolve. The capture of the default this update replaces — the
// explicit selection, or the newest installed bundle when none is selected —
// the publication and the selection happen in one critical section, so two
// overlapping updates each record the default they really replaced. Existing
// sessions continue to resolve their exact pinned version.
func (m *BundleManager) InstallAndSelect(ctx context.Context, harness, version string) (Bundle, error) {
	h, err := bundleHarness(harness)
	if err != nil {
		return Bundle{}, err
	}
	probe := func(ctx context.Context, b Bundle) error {
		if err := m.Probe(ctx, b); err != nil {
			return fmt.Errorf("%s %s failed its startup check and was not selected: %v", b.Harness, b.Version, err)
		}
		return nil
	}
	commit := func(h, version, staging, final string) (Bundle, error) {
		previous := m.effectiveDefault(h)
		if staging != "" && os.Rename(staging, final) != nil {
			return Bundle{}, failure()
		}
		b, err := m.read(h, version)
		if err != nil {
			return Bundle{}, err
		}
		if previous != "" && previous != version {
			if err := m.writeMarker("."+h+"-lastgood", previous); err != nil {
				return Bundle{}, err
			}
		}
		if err := m.selectLocked(h, version); err != nil {
			return Bundle{}, err
		}
		return b, nil
	}
	return m.installCandidate(ctx, h, version, probe, commit)
}

// Rollback re-selects the last known-good version in one command. The last
// good version is the selection an update replaced; GC and Remove keep it on
// disk until it is used. A second rollback without a new update has no
// target and fails without changing the selection.
func (m *BundleManager) Rollback(ctx context.Context, harness string) (Bundle, error) {
	h, err := bundleHarness(harness)
	if err != nil {
		return Bundle{}, err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return Bundle{}, err
	}
	defer unlock()
	previous := m.lastGood(h)
	if previous == "" {
		return Bundle{}, errors.New("No earlier good version is recorded. Install an update first.")
	}
	b, err := m.read(h, previous)
	if err != nil {
		return Bundle{}, err
	}
	if err := m.selectLocked(h, previous); err != nil {
		return Bundle{}, err
	}
	if err := os.Remove(filepath.Join(m.Root, "."+h+"-lastgood")); err != nil && !os.IsNotExist(err) {
		return Bundle{}, failure()
	}
	return b, nil
}

// installPiForkNPM installs Dyna's single tarball after checking its SHA-512.
// Release builds download the stamped asset. Source builds use the stamped
// local path. DYNA_PI_TARBALL explicitly overrides either source for QA.
func installPiForkNPM(ctx context.Context, m *BundleManager, staging, version string, env []string, a RuntimeAdapter) (InstallArtifact, error) {
	tarball := os.Getenv(piForkTarballEnv)
	expected := ""
	if tarball == "" {
		expected = piForkIntegrity
		if !recommendedIntegrity(expected) {
			return InstallArtifact{}, errors.New("Pi fork build metadata is incomplete; set DYNA_PI_TARBALL for development")
		}
		if piForkURL != "" {
			if piForkAsset == "" || filepath.Base(piForkAsset) != piForkAsset {
				return InstallArtifact{}, errors.New("Pi fork release metadata is incomplete")
			}
			tarball = filepath.Join(staging, piForkAsset)
			args := []string{"curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--max-filesize", "134217728", "--output", tarball, piForkURL}
			if RunBundleCommand(ctx, m, args, env) != nil {
				return InstallArtifact{}, errors.New("Pi fork release download failed")
			}
		} else {
			tarball = piForkAsset
		}
	}
	if !filepath.IsAbs(tarball) || filepath.Clean(tarball) != tarball {
		return InstallArtifact{}, errors.New("Pi fork tarball must name an absolute, clean path")
	}
	data, err := os.ReadFile(tarball)
	if err != nil {
		return InstallArtifact{}, errors.New("cannot read the Pi fork tarball")
	}
	sum := sha512.Sum512(data)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	if expected != "" && integrity != expected {
		return InstallArtifact{}, errors.New("Pi fork tarball integrity does not match this Dyna release")
	}
	args := []string{"npm", "install", "--global", "--prefix", staging, "--package-lock", "--ignore-scripts", tarball}
	if RunBundleCommand(ctx, m, args, env) != nil {
		return InstallArtifact{}, errors.New("private bundle installation failed")
	}
	manifestPath := filepath.Join(staging, "lib", "node_modules", piNPMPackage, "package.json")
	data, err = os.ReadFile(manifestPath)
	if err != nil {
		return InstallArtifact{}, errors.New("installed Pi fork package metadata is missing")
	}
	var manifest struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		DynaFork int    `json:"dynaFork"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Name != piNPMPackage || manifest.Version != version {
		return InstallArtifact{}, errors.New("installed Pi fork package has the wrong identity or version")
	}
	if manifest.DynaFork < minimumPiForkLevel {
		return InstallArtifact{}, errors.New("installed Pi package does not meet the required Dyna fork level")
	}
	return InstallArtifact{filepath.Join("bin", a.Binary), piNPMPackage, integrity}, nil
}

// installNPM owns npm installation, script policy, and exact integrity metadata.
func installNPM(ctx context.Context, m *BundleManager, staging, version string, env []string, a RuntimeAdapter) (InstallArtifact, error) {
	if a.NPMPackage == "" {
		return InstallArtifact{}, errors.New("no exact private install recipe is verified for this harness")
	}
	if a.distribution == distributionFork && (version == piForkVersion || isForkBuildVersion(version)) {
		return installPiForkNPM(ctx, m, staging, version, env, a)
	}
	args := []string{"npm", "install", "--global", "--prefix", staging, "--package-lock"}
	// Kimi's postinstall can rename a legacy kimi shim elsewhere on PATH;
	// private bundles must not change anything outside their staging tree.
	if a.Harness == "pi" || a.Harness == "grok" || a.Harness == "kimi-code" {
		args = append(args, "--ignore-scripts")
	}
	args = append(args, a.NPMPackage+"@"+version)
	if RunBundleCommand(ctx, m, args, env) != nil {
		return InstallArtifact{}, errors.New("private bundle installation failed")
	}
	executable := filepath.Join("bin", a.Binary)
	if a.NPMExecutable != nil {
		if rel := a.NPMExecutable(staging); rel != "" {
			executable = rel
		}
	}
	integrity := packageIntegrity(staging, a.NPMPackage, version)
	if integrity == "" {
		// Global npm installs can omit both lockfiles and _integrity. npm
		// still checks downloaded tarballs. Retain the exact official receipt.
		var metadata npmDistributionMetadata
		source := "https://registry.npmjs.org/" + url.PathEscape(a.NPMPackage) + "/" + url.PathEscape(version)
		if err := downloadManifest(ctx, m, staging, env, source, &metadata); err != nil {
			return InstallArtifact{}, err
		}
		if metadata.Name != a.NPMPackage || metadata.Version != version || !recommendedIntegrity(metadata.Dist.Integrity) {
			return InstallArtifact{}, errors.New("npm metadata has no verified integrity for the installed package")
		}
		integrity = metadata.Dist.Integrity
	}
	return InstallArtifact{executable, a.NPMPackage, integrity}, nil
}

// published reuses a bundle that already exists. done is false when the caller
// must install it.
func (m *BundleManager) published(ctx context.Context, h, version, parent, final string) (Bundle, bool, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return Bundle{}, true, err
	}
	defer unlock()
	if err := checkedDir(parent, true); err != nil {
		return Bundle{}, true, err
	}
	if _, err := os.Lstat(final); err == nil {
		b, err := m.read(h, version)
		if !errors.Is(err, errBundleUnusable) {
			return b, true, err
		}
		// An incomplete bundle cannot run, so the install replaces it, unless
		// an open session still holds it.
		live, lerr := m.activeLeases()
		if lerr != nil {
			return Bundle{}, true, lerr
		}
		for _, lease := range live {
			if lh, _ := bundleHarness(lease.Harness); lh == h && lease.Version == version {
				return Bundle{}, true, fmt.Errorf("%w; an open session uses it, so close that session first", err)
			}
		}
		if removeBundle(final) != nil {
			return Bundle{}, true, failure()
		}
		return Bundle{}, false, nil
	} else if !os.IsNotExist(err) {
		return Bundle{}, true, failure()
	}
	return Bundle{}, false, nil
}

// forkTarballVersion reads the exact fork build version from a packed
// tarball's package manifest, so an unstamped binary installs the build the
// tarball carries instead of the level-only fallback. The manifest is small
// but well over four kilobytes, so the read is bounded the same way as every
// version-metadata fetch. A tarball without a valid build version reads as
// absent.
func forkTarballVersion(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", false
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err != nil {
			return "", false
		}
		if header.Name != "package/package.json" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(reader, 64<<10))
		if err != nil {
			return "", false
		}
		var manifest struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &manifest) != nil || !isForkBuildVersion(manifest.Version) {
			return "", false
		}
		return manifest.Version, true
	}
}

// packageIntegrity reads exact package metadata, never an unrelated dependency's
// integrity. npm's global installs can use either lock location or _integrity.
func packageIntegrity(root, pkg, version string) string {
	for _, file := range []string{"package-lock.json", "lib/node_modules/.package-lock.json"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			continue
		}
		var lock struct {
			Packages map[string]struct {
				Version   string `json:"version"`
				Integrity string `json:"integrity"`
			} `json:"packages"`
		}
		if json.Unmarshal(data, &lock) != nil {
			continue
		}
		for _, key := range []string{"node_modules/" + pkg, "lib/node_modules/" + pkg, pkg} {
			if p, ok := lock.Packages[key]; ok && p.Version == version && p.Integrity != "" {
				return p.Integrity
			}
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "lib/node_modules", pkg, "package.json"))
	var p struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Integrity string `json:"_integrity"`
	}
	if json.Unmarshal(data, &p) == nil && p.Name == pkg && p.Version == version {
		return p.Integrity
	}
	return ""
}
func freezeBundle(root string) error {
	return filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return failure()
		}
		if e.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return failure()
		}
		if os.Chmod(path, info.Mode().Perm()&^0222) != nil {
			return failure()
		}
		return nil
	})
}
func removeBundle(root string) error {
	// WalkDir does not follow symlinks. Restore directory write bits for removal.
	_ = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			return os.Chmod(path, 0700)
		}
		return err
	})
	return os.RemoveAll(root)
}
func (m *BundleManager) list(unusable bool) ([]Bundle, error) {
	bundles := []Bundle{}
	for _, adapter := range RuntimeAdapters() {
		found, err := m.listHarness(adapter.Harness, unusable)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, found...)
	}
	sortBundles(bundles)
	return bundles, nil
}

// sortBundles orders bundles by harness, then newest version first.
func sortBundles(bundles []Bundle) {
	sort.Slice(bundles, func(i, j int) bool {
		if bundles[i].Harness != bundles[j].Harness {
			return bundles[i].Harness < bundles[j].Harness
		}
		c := semver.Compare("v"+bundles[i].Version, "v"+bundles[j].Version)
		if c == 0 && bundles[i].Version != bundles[j].Version {
			c = strings.Compare(bundles[i].Version, bundles[j].Version)
		}
		if c == 0 {
			return bundles[i].Version > bundles[j].Version
		}
		return c > 0
	})
}

// listHarness reads one harness's installed bundles. Each read hashes the
// bundle's executable, so callers that need one harness must not read all.
// Unusable bundles are skipped unless unusable is set, for cleanup.
func (m *BundleManager) listHarness(h string, unusable bool) ([]Bundle, error) {
	dir := filepath.Join(m.Root, h)
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	if err := checkedDir(dir, false); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, failure()
	}
	bundles := []Bundle{}
	for _, entry := range entries {
		if !validVersion(entry.Name()) {
			continue
		}
		b, err := m.read(h, entry.Name())
		if errors.Is(err, errBundleUnusable) {
			if !unusable {
				continue
			}
		} else if err != nil {
			return nil, err
		}
		bundles = append(bundles, b)
	}
	return bundles, nil
}
func (m *BundleManager) List() ([]Bundle, error) {
	if _, err := os.Stat(m.Root); os.IsNotExist(err) {
		return []Bundle{}, nil
	}
	return m.list(false)
}
func (m *BundleManager) selected(h string) string {
	return m.markerValue("." + h + "-selected")
}

// lastGood reads the rollback target: the selection an update replaced.
func (m *BundleManager) lastGood(h string) string {
	return m.markerValue("." + h + "-lastgood")
}

// effectiveDefault names the version Resolve would hand out now: the explicit
// selection, or the newest installed bundle when none is selected. An update
// records this as its rollback target, so even a marker-less first-use
// install keeps a rollback path.
func (m *BundleManager) effectiveDefault(h string) string {
	if v := m.selected(h); v != "" {
		return v
	}
	bundles, err := m.listHarness(h, false)
	if err != nil || len(bundles) == 0 {
		return ""
	}
	sortBundles(bundles)
	return bundles[0].Version
}

// markerValue reads one plain-version marker file, like the selection or the
// rollback target. An unreadable or invalid marker reads as empty.
func (m *BundleManager) markerValue(name string) string {
	fd, err := syscall.Open(filepath.Join(m.Root, name), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	f := os.NewFile(uintptr(fd), "bundle-selection")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return ""
	}
	v := string(data)
	if !validVersion(v) {
		return ""
	}
	return v
}

// Resolve uses the explicit selection, or the newest installed version when no
// selection exists. A missing explicit version never falls back to the system.
func (m *BundleManager) Resolve(harness, version string) (Bundle, error) {
	h, err := bundleHarness(harness)
	if err != nil {
		return Bundle{}, err
	}
	if version != "" && !validVersion(version) {
		return Bundle{}, errors.New("private bundles require an exact version, not a moving tag or range")
	}
	if version != "" {
		return m.read(h, version)
	}
	if selected := m.selected(h); selected != "" {
		if ok, _ := supportedVersionRange(h, selected); ok {
			return m.read(h, selected)
		}
		// A selection outside the supported range must not pin every new
		// session to a bundle that cannot run. Resolution falls through to
		// the newest supported bundle, so first use installs the fork.
	}
	bundles, err := m.listHarness(h, false)
	if err != nil {
		return Bundle{}, err
	}
	if len(bundles) == 0 {
		return Bundle{}, errors.New("private bundle is not installed")
	}
	sortBundles(bundles)
	return bundles[0], nil
}
func (m *BundleManager) Select(harness, version string) error {
	h, err := bundleHarness(harness)
	if err != nil {
		return err
	}
	if !validVersion(version) {
		return errors.New("private bundles require an exact version, not a moving tag or range")
	}
	unlock, err := m.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return m.selectLocked(h, version)
}

// selectLocked switches the default selection. The caller holds the lock.
func (m *BundleManager) selectLocked(h, version string) error {
	if _, err := m.read(h, version); err != nil {
		return err
	}
	return m.writeMarker("."+h+"-selected", version)
}

// writeMarker atomically publishes one plain-version marker file, like the
// selection and the rollback target.
func (m *BundleManager) writeMarker(name, version string) error {
	f, err := os.CreateTemp(m.Root, ".select-")
	if err != nil {
		return failure()
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(version)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return failure()
	}
	if os.Rename(f.Name(), filepath.Join(m.Root, name)) != nil {
		return failure()
	}
	return nil
}

// GC keeps the newest two versions per harness, explicit selections, and exact
// bundles pinned by open sessions or active native processes.
func (m *BundleManager) GC(pinned []PinnedBundle) error {
	unlock, err := m.lock(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	bundles, err := m.list(true)
	if err != nil {
		return err
	}
	live, err := m.activeLeases()
	if err != nil {
		return err
	}
	pinned = append(pinned, live...)
	counts := map[string]int{}
	for _, b := range bundles {
		counts[b.Harness]++
		keep := counts[b.Harness] <= 2 || m.selected(b.Harness) == b.Version || m.lastGood(b.Harness) == b.Version
		for _, p := range pinned {
			h, e := bundleHarness(p.Harness)
			if e == nil && h == b.Harness && p.Version == b.Version {
				keep = true
			}
		}
		if !keep {
			if removeBundle(b.Directory) != nil {
				return failure()
			}
		}
	}
	return nil
}

// downloadManifest fetches one official JSON manifest over HTTPS into staging,
// decodes it into v and removes the file. Manifests are small; larger bodies
// are rejected.
func downloadManifest(ctx context.Context, m *BundleManager, staging string, env []string, source string, v any) error {
	file, err := os.CreateTemp(staging, ".manifest-*.json")
	if err != nil {
		return failure()
	}
	path := file.Name()
	defer os.Remove(path)
	if file.Close() != nil {
		return failure()
	}
	args := []string{"curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--max-filesize", "65536", "--output", path, source}
	if RunBundleCommand(ctx, m, args, env) != nil {
		return errors.New("official release manifest download failed")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, v) != nil {
		return errors.New("official release manifest is unreadable")
	}
	return nil
}

func runBundleCommand(ctx context.Context, args, env []string) error {
	if len(args) == 0 {
		return failure()
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = env
	return cmd.Run()
}

type bundleUse struct {
	PID    int    `json:"pid"`
	Bundle Bundle `json:"bundle"`
}

// Lease prevents garbage collection during workflows as well as durable sessions.
func (m *BundleManager) Lease(ctx context.Context, b Bundle) (func(), error) {
	lease, err := m.LeaseManaged(ctx, b)
	if err != nil {
		return nil, err
	}
	return func() { _ = lease.Close() }, nil
}

// LeaseManaged binds bundle protection to the runner and its guarded process.
func (m *BundleManager) LeaseManaged(ctx context.Context, b Bundle) (*ResourceLease, error) {
	unlock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if _, err = m.read(b.Harness, b.Version); err != nil {
		return nil, err
	}
	return newResourceLease(ctx, filepath.Join(m.Root, ".leases"), "use-", b)
}
func (m *BundleManager) activeLeases() ([]PinnedBundle, error) {
	root := filepath.Join(m.Root, ".leases")
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []PinnedBundle{}
	paths, err := resourceLeasePaths(root, "use-")
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		live, use, e := resourceLeaseState(path)
		if e != nil {
			return nil, e
		}
		if !live {
			removeResourceLease(path)
			continue
		}
		var bundle Bundle
		if json.Unmarshal(use.Data, &bundle) != nil || bundle.Harness == "" || bundle.Version == "" {
			return nil, failure()
		}
		out = append(out, PinnedBundle{Bundle: bundle, Holder: leaseHolder(use)})
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "turn-") {
			continue
		}
		path := filepath.Join(root, e.Name())
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return nil, failure()
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, failure()
		}
		var use bundleUse
		if json.Unmarshal(raw, &use) != nil || use.PID <= 0 {
			return nil, failure()
		}
		if syscall.Kill(use.PID, 0) == syscall.ESRCH {
			_ = os.Remove(path)
			continue
		}
		out = append(out, PinnedBundle{Bundle: use.Bundle, Holder: fmt.Sprintf("Turn PID %d", use.PID)})
	}
	return out, nil
}

// Remove deletes only an unused Dyna-owned bundle. Account slots, native homes,
// configuration generations and legacy recovery copies are outside its target.
func (m *BundleManager) Remove(ctx context.Context, harness, version string, pinned []PinnedBundle, confirmed bool) error {
	if !confirmed {
		return errors.New("Confirm private bundle removal first.")
	}
	h, err := bundleHarness(harness)
	if err != nil {
		return err
	}
	if !validVersion(version) {
		return errors.New("Private bundle removal requires an exact version.")
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	bundle, err := m.read(h, version)
	if err != nil && !errors.Is(err, errBundleUnusable) {
		return err
	}
	if m.selected(h) == version {
		return errors.New("Select another private version before removing this selected bundle.")
	}
	if m.lastGood(h) == version {
		return errors.New("This version is the rollback target. Roll back or update again before removing it.")
	}
	if err = m.checkUnused(h, []Bundle{bundle}, pinned); err != nil {
		return err
	}
	if err = removeBundle(bundle.Directory); err != nil {
		return failure()
	}
	return nil
}

// Uninstall removes every Dyna copy of a harness and its selection, so no
// older copy takes over. The next sign-in or session installs it again. Saved
// accounts stay.
func (m *BundleManager) Uninstall(ctx context.Context, harness string, pinned []PinnedBundle, confirmed bool) error {
	if !confirmed {
		return errors.New("Confirm private bundle removal first.")
	}
	h, err := bundleHarness(harness)
	if err != nil {
		return err
	}
	unlock, err := m.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	bundles, err := m.listHarness(h, true)
	if err != nil {
		return err
	}
	if err = m.checkUnused(h, bundles, pinned); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(m.Root, "."+h+"-selected")); err != nil && !os.IsNotExist(err) {
		return failure()
	}
	if err = os.Remove(filepath.Join(m.Root, "."+h+"-lastgood")); err != nil && !os.IsNotExist(err) {
		return failure()
	}
	for _, b := range bundles {
		if err = removeBundle(b.Directory); err != nil {
			return failure()
		}
	}
	return nil
}

// checkUnused fails when a profile pin or live lease uses one of harness h's
// bundles, naming the profile, session or process that holds it.
func (m *BundleManager) checkUnused(h string, bundles []Bundle, pinned []PinnedBundle) error {
	live, err := m.activeLeases()
	if err != nil {
		return err
	}
	for _, pin := range append(append([]PinnedBundle{}, pinned...), live...) {
		canonical, e := bundleHarness(pin.Harness)
		for _, b := range bundles {
			if e == nil && canonical == h && pin.Version == b.Version {
				return fmt.Errorf("%s still uses this private bundle.", pin.Holder)
			}
		}
	}
	return nil
}
