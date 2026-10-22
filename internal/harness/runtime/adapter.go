package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	harnessmgr "dyna-agent/internal/harness/config"
)

// RuntimeAdapter describes a harness's private native scope. Each provider lives
// in its own file. Shared code owns manifests, catalog generation and launch.
type RuntimeAdapter struct {
	Harness             string
	Binary              string
	NPMPackage          string
	ConfigFormat        string // json, opencode, or toml
	MCPKey              string
	ConfigFile          string // relative native config path, when no generation flag exists
	SkillDir            string // relative native skill directory
	NativeEnv           func(home string) map[string]string
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
	// Exclusive serializes turns on one account home. Turns share the lease
	// unless this is set, and ExclusiveReason names the verified conflict.
	Exclusive       bool
	ExclusiveReason string
	Install         func(context.Context, *BundleManager, string, string, []string) (InstallArtifact, error)
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
	if a.Harness == "" || a.Binary == "" {
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

// RuntimeAdapters returns every registered harness adapter, sorted.
func RuntimeAdapters() []RuntimeAdapter {
	adapters.RLock()
	defer adapters.RUnlock()
	out := []RuntimeAdapter{}
	for _, a := range adapters.items {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Harness < out[j].Harness })
	return out
}

func canonicalRuntimeID(h string) string { return harnessmgr.CanonicalHarnessID(h) }

// RunBundleCommand runs installer argv with the supplied environment. Tests use
// the manager's fake runner; implementations must not invoke a shell implicitly.
func RunBundleCommand(ctx context.Context, m *BundleManager, args, env []string) error {
	if m.Run != nil {
		return m.Run(ctx, args, env)
	}
	return runBundleCommand(ctx, args, env)
}
