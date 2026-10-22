package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Grok Build keeps its native scope under GROK_HOME: config.toml holds
// [mcp_servers], skills live in skills/, and credentials are auth.json
// (docs.x.ai/build/settings, docs.x.ai/build/settings/reference). The npm
// package carries a compressed native binary. Dyna extracts it at install time
// so sessions keep their pinned binary. Grok runs with its own defaults: Dyna
// imports no personal preferences from ~/.grok/config.toml.
func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness:      "grok",
		Install:      installGrok,
		Binary:       "grok",
		ConfigFormat: "toml",
		MCPKey:       "mcp_servers",
		ConfigFile:   "config.toml",
		SkillDir:     "skills",
		// Grok loads $GROK_HOME/AGENTS.md as global rules.
		AuthFiles: []string{"auth.json"},
		NativeEnv: func(home string) map[string]string {
			return map[string]string{
				"GROK_HOME":                home,
				"GROK_DISABLE_AUTOUPDATER": "1",
				// Clear inherited personal provider configuration. An empty
				// GROK_CONFIG is documented to count as unset.
				"XAI_API_KEY":           "",
				"GROK_XAI_API_BASE_URL": "",
				"GROK_MODELS_BASE_URL":  "",
				"GROK_MODELS_LIST_URL":  "",
				"GROK_DEFAULT_MODEL":    "",
				"GROK_WEB_SEARCH_MODEL": "",
				"GROK_CONFIG":           "",
				"GROK_CONFIG_PATH":      "",
			}
		},
		// Grok has no native flag for a config or skill generation path, so
		// shared code materializes ConfigFile and SkillDir in the account home.
		// Grok 1.0.62 accepts hidden top-level --no-leader before both
		// headless options and login. Keep the flag before the subcommand;
		// `grok login --no-leader` is rejected by the native parser.
		// The private socket also isolates any older leader already running.
		ConfigArgs: func(home, configPath, skillPath string) []string {
			return []string{"--no-auto-update", "--no-leader", "--leader-socket", filepath.Join(home, "leader.sock")}
		},
		LoginArgs: func(executable, home string) []string {
			return []string{executable, "--no-leader", "login", "--leader-socket", filepath.Join(home, "leader.sock")}
		},
		// With --no-leader each process runs its own embedded agent. Grok
		// guards auth.json refresh with its own cross-process lock, and six
		// processes started at once on one GROK_HOME each created a session
		// in under 250 ms (Grok 1.0.62).
	})
}

// installGrok bypasses the npm launcher. That launcher prefers GROK_HOME/bin/grok
// and can run a different version from the session's pinned bundle.
func installGrok(ctx context.Context, m *BundleManager, staging, version string, env []string) (InstallArtifact, error) {
	a, _ := Adapter("grok")
	artifact, err := installNPM(ctx, m, staging, version, env, a)
	if err != nil {
		return InstallArtifact{}, err
	}
	if native := npmPlatformExecutable(staging, a.NPMPackage, filepath.Join("bin", "grok"), ""); native != "" {
		artifact.Executable = native
		return artifact, nil
	}
	compressed := npmPlatformExecutable(staging, a.NPMPackage, filepath.Join("bin", "grok.br"), "")
	if compressed == "" {
		return InstallArtifact{}, errors.New("Grok platform package has no native binary")
	}
	if err := validateTree(staging); err != nil {
		return InstallArtifact{}, err
	}
	artifact.Executable = filepath.Join("bin", "grok-native")
	if err := os.MkdirAll(filepath.Join(staging, "bin"), 0700); err != nil {
		return InstallArtifact{}, failure()
	}
	script := `const fs = require('node:fs'); const zlib = require('node:zlib'); fs.writeFileSync(process.argv[2], zlib.brotliDecompressSync(fs.readFileSync(process.argv[1])), {mode: 0o755, flag: 'wx'});`
	args := []string{"node", "-e", script, filepath.Join(staging, compressed), filepath.Join(staging, artifact.Executable)}
	if RunBundleCommand(ctx, m, args, env) != nil {
		return InstallArtifact{}, errors.New("Grok native binary extraction failed")
	}
	return artifact, nil
}
