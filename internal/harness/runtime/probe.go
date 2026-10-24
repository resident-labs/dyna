package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/harness/cliversion"
	"dyna-agent/internal/harness/home"
	"dyna-agent/internal/profile"
)

// defaultProbeTimeout bounds one startup probe. Wrapped npm CLIs can take
// several seconds to start cold, so the budget stays far below the
// five-minute install budget it runs inside.
const defaultProbeTimeout = 30 * time.Second

// Probe runs one no-cost startup check against an installed bundle: the
// executable must start and answer its version command. It never makes a
// provider call and never reads or writes personal files: the run gets a
// throwaway private scope, like an install. A pass is required before the
// bundle becomes the default for new sessions.
func (m *BundleManager) Probe(ctx context.Context, b Bundle) error {
	a, err := Adapter(b.Harness)
	if err != nil {
		return err
	}
	args := []string{"--version"}
	if d, ok := profile.HarnessDefinitionFor(a.Harness); ok && len(d.VersionArgs) > 0 {
		args = d.VersionArgs
	}
	// One parent holds both the scope and its paired .home, so both go away
	// together. Install stages its private roots the same way.
	parent, err := os.MkdirTemp(m.Root, ".probe-")
	if err != nil {
		return failure()
	}
	defer os.RemoveAll(parent)
	scope := filepath.Join(parent, "scope")
	if err := home.Ensure(scope); err != nil {
		return err
	}
	env, err := probeEnvironment(a, scope)
	if err != nil {
		return err
	}
	timeout := m.ProbeTimeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	reported, err := cliversion.Run(ctx, b.Executable, args, env, timeout)
	if err != nil {
		if errors.Is(err, cliversion.ErrCanceled) {
			return errors.New("the new version did not answer its startup check in time")
		}
		return errors.New("the new version did not start or exited with an error")
	}
	if ok, reason := supportedVersionRange(b.Harness, reported); !ok {
		return errors.New("the new version reported an unsupported version: " + reason)
	}
	return nil
}

// probeEnvironment builds the environment for one probe run. It keeps owner
// toolchains (PATH, node, go) available but points HOME, the XDG roots and
// the adapter's native roots at a private throwaway scope.
func probeEnvironment(a RuntimeAdapter, scope string) ([]string, error) {
	base := NativeEnvironmentBase(a.Harness, os.Environ())
	tools, err := home.ToolRoots(base)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(base))
	for _, item := range base {
		if key, value, ok := strings.Cut(item, "="); ok {
			values[key] = value
		}
	}
	for key, value := range tools {
		values[key] = value
	}
	for key, value := range home.Roots(scope) {
		values[key] = value
	}
	if a.NativeEnv != nil {
		for key, value := range a.NativeEnv(scope) {
			values[key] = value
		}
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env, nil
}
