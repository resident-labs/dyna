package runtime

import (
	"path/filepath"
	"runtime"

	harnesshome "dyna-agent/internal/harness/home"

	"golang.org/x/sys/cpu"
)

// This recipe targets OpenCode v2 (@opencode/cli), not v1 (opencode-ai).
// The inspected v2 CLI supports --standalone; v1 must not receive that flag.
// Evidence: https://opencode.ai/v2/docs/config and /v2/docs/skills, checked
// 2026-10-17, and the approved private-harness-isolation.md binary inspection.
// V2 uses update="disable" and mcp.servers; v1 uses autoupdate=false and mcp.
// The config generator must keep those schemas separate.
func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness:      "opencode",
		SkillsPinned: true,
		Binary:       "opencode",
		ConfigFormat: "opencode",
		MCPKey:       "mcp",
		ConfigFile:   "opencode.json",
		SkillDir:     "skills",
		// OpenCode loads $OPENCODE_CONFIG_DIR/AGENTS.md as global rules.
		// V2 documents username as a display preference, with no execution or
		// provider behavior. Do not import v1-only keys such as theme here.
		ConfigAllowlist: []string{"username"},
		PersonalConfigFiles: []string{
			".config/opencode/opencode.json",
			".config/opencode/opencode.jsonc",
		},
		NativeEnv: func(home string) map[string]string {
			return map[string]string{
				"OPENCODE_CONFIG_DIR":         home,
				"OPENCODE_DB":                 filepath.Join(home, "opencode.db"),
				"OPENCODE_DISABLE_AUTOUPDATE": "1",
				// Both overrides are verified in the inspected v2 binary. Clear
				// inherited config layers that could replace Dyna's settings.
				"OPENCODE_CONFIG":         "",
				"OPENCODE_CONFIG_CONTENT": harnesshome.OpenCodeEmptyConfigContent,
			}
		},
		// The shared generation builder sets OPENCODE_CONFIG to the immutable
		// generation file after NativeEnv clears inherited config overrides.
		// Fresh auth uses the private database. Never copy opencode/v2-ref@1
		// from the personal scope or treat database existence as signed in.
		LoginArgs: func(executable, _ string) []string { return []string{executable, "auth", "login"} },
		// Turns share the account lease because managed-service clients share
		// one SQLite writer. Standalone servers must not write this database
		// concurrently.
		NPMExecutable: func(root string) string {
			suffixes := openCodeVariants(runtime.GOOS, runtime.GOARCH, cpu.X86.HasAVX2, muslLoader())
			return npmPlatformExecutable(root, "@opencode/cli", filepath.Join("bin", "opencode"), suffixes...)
		},
	})
}

// openCodeVariants lists platform package suffixes in the order of
// @opencode/cli 2.0.32 postinstall.mjs packageNames(): -baseline builds run
// on x64 CPUs without AVX2, and -musl builds on musl Linux. Dyna reads AVX2
// from CPUID (cpu.X86.HasAVX2), which also checks OS support, on every OS.
func openCodeVariants(goos, goarch string, avx2, musl bool) []string {
	x64 := goarch == "amd64"
	switch {
	case goos == "linux" && musl && x64 && !avx2:
		return []string{"-baseline-musl", "-musl", "-baseline", ""}
	case goos == "linux" && musl && x64:
		return []string{"-musl", "-baseline-musl", "", "-baseline"}
	case goos == "linux" && musl:
		return []string{"-musl", ""}
	case goos == "linux" && x64 && !avx2:
		return []string{"-baseline", "", "-baseline-musl", "-musl"}
	case goos == "linux" && x64:
		return []string{"", "-baseline", "-musl", "-baseline-musl"}
	case goos == "linux":
		return []string{"", "-musl"}
	case x64 && !avx2:
		return []string{"-baseline", ""}
	case x64:
		return []string{"", "-baseline"}
	}
	return []string{""}
}

// muslLoader reports musl Linux by its dynamic loader, which every musl
// binary needs. Dyna runs no command to detect it.
func muslLoader() bool {
	matches, _ := filepath.Glob("/lib/ld-musl-*.so.1")
	return len(matches) > 0
}
