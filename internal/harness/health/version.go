package health

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/harness/cliversion"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

// VersionInfo describes executable discovery and a best-effort CLI version.
// Error contains only fixed messages, never command output or environment values.
type VersionInfo struct {
	Version   string `json:"version"`
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	// Installable is true when Dyna installs its private copy on first use.
	Installable bool   `json:"installable,omitempty"`
	Error       string `json:"error,omitempty"`
}

const versionCacheTTL = 20 * time.Second

type versionKey struct {
	path     string
	modified int64
	size     int64
	env      [32]byte
	harness  string
}
type versionEntry struct {
	ready   chan struct{}
	expires time.Time
	result  VersionInfo
}

var versions = struct {
	sync.Mutex
	entries map[versionKey]*versionEntry
}{entries: make(map[versionKey]*versionEntry)}

// HarnessReady is runtime.HarnessReady. Tests replace it so they make
// no version-metadata requests.
var HarnessReady = runtime.HarnessReady

// VersionDetector permits hermetic integration tests to replace CLI discovery.
// Set it only during process initialization, before concurrent callers start.
var VersionDetector = DetectVersion

// DetectVersion resolves the effective launch environment on every call. Native
// version commands have a two-second deadline and a short, process-local cache.
// Custom commands are only located; mock harnesses require no executable.
func DetectVersion(ctx context.Context, p profile.Profile) VersionInfo {
	return detectVersion(ctx, p, 2*time.Second)
}

func detectVersion(ctx context.Context, p profile.Profile, timeout time.Duration) VersionInfo {
	if p.Harness == profile.HarnessMock {
		return VersionInfo{Version: "built-in", Installed: true}
	}
	definition, ok := profile.HarnessDefinitionFor(p.Harness)
	if !ok {
		return VersionInfo{Error: "unknown harness"}
	}
	env := versionEnvironment(p.Env)
	var path string
	var err error
	if p.Harness == profile.HarnessCustom {
		result := VersionInfo{Version: "custom"}
		if len(p.Command) > 0 {
			path = customVersionPath(p.Command[0], env)
		}
		result.Path, result.Installed = path, path != ""
		if !result.Installed {
			result.Error = "executable not found"
		}
		return result
	}
	// Dyna runs its private copy: the prepared executable, else the bundle.
	path = p.RuntimeExecutable
	if path == "" {
		b, err := runtime.NewBundleManager().Resolve(p.Harness, p.RuntimeVersion)
		if err != nil {
			if _, readyErr := HarnessReady(ctx, p.Harness, p.RuntimeVersion); readyErr != nil {
				return VersionInfo{Error: readyErr.Error()}
			}
			return VersionInfo{Installable: true, Error: "Dyna installs this harness on first use."}
		}
		path = b.Executable
	}
	result := VersionInfo{Path: path, Installed: true}
	info, err := os.Stat(path)
	if err != nil {
		result.Error = "executable unavailable"
		return result
	}
	encoded, _ := json.Marshal(env)
	key := versionKey{path, info.ModTime().UnixNano(), info.Size(), sha256.Sum256(encoded), p.Harness}
	versions.Lock()
	now := time.Now()
	for k, entry := range versions.entries {
		if !entry.expires.IsZero() && !now.Before(entry.expires) {
			delete(versions.entries, k)
		}
	}
	if entry := versions.entries[key]; entry != nil {
		versions.Unlock()
		select {
		case <-entry.ready:
			return entry.result
		case <-ctx.Done():
			result.Error = "version detection canceled"
			return result
		}
	}
	// Limit completed cache entries even when many environments change rapidly.
	if len(versions.entries) >= 256 {
		for k, entry := range versions.entries {
			if !entry.expires.IsZero() {
				delete(versions.entries, k)
				break
			}
		}
	}
	entry := &versionEntry{ready: make(chan struct{})}
	versions.entries[key] = entry
	versions.Unlock()
	result = runVersion(ctx, path, definition.VersionArgs, env, timeout)
	versions.Lock()
	entry.result, entry.expires = result, time.Now().Add(versionCacheTTL)
	// A caller's cancellation must not poison the next independent view.
	if ctx.Err() != nil {
		delete(versions.entries, key)
	}
	close(entry.ready)
	versions.Unlock()
	return result
}

func versionEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

func customVersionPath(binary string, env []string) string {
	if binary == "" {
		return ""
	}
	candidate := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return ""
		}
		info, err := os.Stat(absolute)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return absolute
		}
		return ""
	}
	if strings.ContainsRune(binary, filepath.Separator) {
		return candidate(binary)
	}
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "PATH="); ok {
			for _, dir := range filepath.SplitList(value) {
				if !filepath.IsAbs(dir) {
					continue
				}
				if path := candidate(filepath.Join(dir, binary)); path != "" {
					return path
				}
			}
		}
	}
	return ""
}

func runVersion(parent context.Context, path string, args, env []string, timeout time.Duration) VersionInfo {
	result := VersionInfo{Path: path, Installed: true}
	version, err := cliversion.Run(parent, path, args, env, timeout)
	switch {
	case err == nil:
		result.Version = version
	case errors.Is(err, cliversion.ErrCanceled):
		result.Error = "version detection timed out or canceled"
	case errors.Is(err, cliversion.ErrNoVersion):
		result.Error = "version output not recognized"
	default:
		result.Error = "version command failed"
	}
	return result
}
