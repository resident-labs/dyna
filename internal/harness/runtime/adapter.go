package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	harnessmgr "dyna-agent/internal/harness/config"
)

// RuntimeAdapter describes a harness's private native scope. Each provider lives
// in its own file. Shared code owns manifests, catalog generation and launch.
type RuntimeAdapter struct {
	Harness      string
	Binary       string
	NPMPackage   string
	ConfigFormat string // json or toml
	MCPKey       string
	ConfigFile   string // relative native config path, when no generation flag exists
	SkillDir     string // relative native skill directory
	NativeEnv    func(home string) map[string]string
	// UnsetEnv removes inherited credentials, endpoints and personal model
	// settings before NativeEnv and explicit profile values are applied.
	UnsetEnv            []string
	ConfigArgs          func(home, configPath, skillPath string) []string
	LoginArgs           func(executable, home string) []string
	AuthFiles           []string // native relative paths for an offline sign-in check
	ConfigAllowlist     []string
	PersonalConfigFiles []string
	AccountBlocked      string
	PartialReasons      []string
	// SkillsPinned is true when each generation passes its own skill set,
	// so a resumed session keeps the skills it started with.
	SkillsPinned bool
	// Requires names the files a bundle must hold beside its executable. A
	// bundle without them is never resolved, selected or reused.
	Requires []string
	// Exclusive serializes turns on one account home. Turns share the lease
	// unless this is set, and ExclusiveReason names the verified conflict.
	Exclusive       bool
	ExclusiveReason string
	// AccountOnly marks a retired harness whose saved accounts and usage
	// reader stay. It keeps the native home safe and refreshable, and it is
	// never listed, installed, leased or run as a harness.
	AccountOnly bool
	Install     func(context.Context, *BundleManager, string, string, []string) (InstallArtifact, error)
	// NPMExecutable, when set, picks the executable inside an npm install
	// root. An empty result keeps bin/<Binary>.
	NPMExecutable func(root string) string
}

// npmPlatformExecutable returns the path of binary inside the first installed
// platform dependency <pkg>-<os>-<arch><suffix>, trying suffixes in order. npm 12
// blocks the postinstall script that copies it over the package's bin stub,
// so the stub alone only prints an error.
func npmPlatformExecutable(root, pkg, binary string, suffixes ...string) string {
	platform := map[string]string{"linux/amd64": "linux-x64", "linux/arm64": "linux-arm64", "darwin/amd64": "darwin-x64", "darwin/arm64": "darwin-arm64"}[runtime.GOOS+"/"+runtime.GOARCH]
	if platform == "" {
		return ""
	}
	for _, suffix := range suffixes {
		native := filepath.Join(pkg+"-"+platform+suffix, binary)
		for _, rel := range []string{
			filepath.Join("lib", "node_modules", pkg, "node_modules", native),
			filepath.Join("lib", "node_modules", native),
		} {
			if info, err := os.Lstat(filepath.Join(root, rel)); err == nil && info.Mode().IsRegular() {
				return rel
			}
		}
	}
	return ""
}

type InstallArtifact struct{ Executable, Package, Integrity string }

var adapters = struct {
	sync.RWMutex
	items map[string]RuntimeAdapter
}{items: map[string]RuntimeAdapter{}}

func RegisterRuntimeAdapter(a RuntimeAdapter) {
	adapters.Lock()
	defer adapters.Unlock()
	if a.Harness == "" || (a.Binary == "" && !a.AccountOnly) {
		panic("runtime adapter needs a harness and binary")
	}
	if _, ok := adapters.items[a.Harness]; ok {
		panic("duplicate runtime adapter")
	}
	a.NPMPackage = harnessDistributions[a.Harness].Package
	a.PartialReasons = append(a.PartialReasons, nativeFeaturePolicies[a.Harness].NoSwitch...)
	adapters.items[a.Harness] = a
}

var errNoAdapter = errors.New("private runtime adapter is not available for this harness")

// Adapter returns a harness's adapter.
func Adapter(h string) (RuntimeAdapter, error) {
	h = canonicalRuntimeID(h)
	adapters.RLock()
	defer adapters.RUnlock()
	a, ok := adapters.items[h]
	if !ok {
		return a, errNoAdapter
	}
	return a, nil
}

// RunsAsHarness reports whether a turn can run h. A retired harness keeps only
// its saved accounts and usage reader, so account reads must not require its
// bundle or executable.
func RunsAsHarness(h string) bool {
	a, err := Adapter(h)
	return err == nil && !a.AccountOnly
}

// RuntimeAdapters returns every registered harness adapter, sorted. Adapters
// for retired harnesses are excluded: their accounts stay, their turns do not.
func RuntimeAdapters() []RuntimeAdapter {
	adapters.RLock()
	defer adapters.RUnlock()
	out := []RuntimeAdapter{}
	for _, a := range adapters.items {
		if a.AccountOnly {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Harness < out[j].Harness })
	return out
}

func canonicalRuntimeID(h string) string { return harnessmgr.CanonicalHarnessID(h) }

// NativeEnvironmentBase removes inherited values that a native harness must
// never read. Callers merge account and profile overrides after this filter.
func NativeEnvironmentBase(h string, environ []string) []string {
	a, err := Adapter(h)
	if err != nil || len(a.UnsetEnv) == 0 {
		return environ
	}
	blocked := make(map[string]bool, len(a.UnsetEnv))
	for _, key := range a.UnsetEnv {
		blocked[key] = true
	}
	out := make([]string, 0, len(environ))
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			out = append(out, item)
		}
	}
	return out
}

// RunBundleCommand runs installer argv with the supplied environment. Tests use
// the manager's fake runner; implementations must not invoke a shell implicitly.
func RunBundleCommand(ctx context.Context, m *BundleManager, args, env []string) error {
	if m.Run != nil {
		return m.Run(ctx, args, env)
	}
	return runBundleCommand(ctx, args, env)
}
