package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	harnessmgr "dyna-agent/internal/harness/config"

	"golang.org/x/sys/cpu"
)

func TestOpenCodeV2PrivateScope(t *testing.T) {
	personal := t.TempDir()
	t.Setenv("HOME", personal)
	t.Setenv("XDG_DATA_HOME", filepath.Join(personal, "data"))
	home := filepath.Join(t.TempDir(), "private account ; $(unused)")
	a, err := Adapter("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if a.Harness != "opencode" || a.Binary != "opencode" || a.NPMPackage != "@opencode/cli" || a.Install != nil {
		t.Fatalf("unexpected v2 recipe: %+v", a)
	}
	if a.ConfigFormat != "opencode" || a.MCPKey != "mcp" || a.ConfigFile != "opencode.json" || a.SkillDir != "skills" {
		t.Fatalf("unexpected config contract: %+v", a)
	}
	env := a.NativeEnv(home)
	want := map[string]string{
		"OPENCODE_CONFIG_DIR": home, "OPENCODE_DB": filepath.Join(home, "opencode.db"),
		"OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": "{}",
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("native environment = %v, want %v", env, want)
	}
	for key := range env {
		if key == "HOME" || strings.HasPrefix(key, "XDG_") {
			t.Fatalf("native environment changes personal root %s", key)
		}
	}
	for _, rel := range []string{a.ConfigFile, a.SkillDir} {
		if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			t.Fatalf("native path escapes private home: %q", rel)
		}
	}
	if a.ConfigArgs != nil {
		t.Fatal("private turns must use the managed service without standalone arguments")
	}
	if os.Getenv("HOME") != personal || os.Getenv("XDG_DATA_HOME") != filepath.Join(personal, "data") {
		t.Fatal("adapter changed the calling environment")
	}
}

func TestOpenCodeV2ReportsUnverifiedFeatures(t *testing.T) {
	a, err := Adapter("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountBlocked != "" || a.LoginArgs == nil || len(a.AuthFiles) != 0 || a.Exclusive {
		t.Fatal("adapter must allow fresh login, require native readiness checks, and allow concurrent turns")
	}
	executable := filepath.Join(t.TempDir(), "private executable ; $(unused)")
	if args := a.LoginArgs(executable, t.TempDir()); !reflect.DeepEqual(args, []string{executable, "auth", "login"}) {
		t.Fatalf("fresh native login args = %q", args)
	}
	if len(a.PartialReasons) != 0 {
		t.Fatalf("private HOME and XDG remove the personal discovery limits: %v", a.PartialReasons)
	}
	if !reflect.DeepEqual(a.ConfigAllowlist, []string{"username"}) || !reflect.DeepEqual(a.PersonalConfigFiles, []string{".config/opencode/opencode.json", ".config/opencode/opencode.jsonc"}) {
		t.Fatal("personal import must include only verified harmless v2 preferences")
	}
}

func TestOpenCodeUsesPlatformBinary(t *testing.T) {
	a, _ := Adapter("opencode")
	if a.NPMExecutable == nil {
		t.Fatal("OpenCode keeps the bin stub that needs its blocked postinstall script")
	}
	root := t.TempDir()
	if got := a.NPMExecutable(root); got != "" {
		t.Fatalf("missing platform binary picked %q", got)
	}
	platform := map[string]string{"linux/amd64": "linux-x64", "linux/arm64": "linux-arm64", "darwin/amd64": "darwin-x64", "darwin/arm64": "darwin-arm64"}[runtime.GOOS+"/"+runtime.GOARCH]
	if platform == "" {
		t.Skip("no OpenCode platform package")
	}
	rel := func(suffix string) string {
		return filepath.Join("lib", "node_modules", "@opencode", "cli", "node_modules", "@opencode", "cli-"+platform+suffix, "bin", "opencode")
	}
	write := func(suffix string) {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel(suffix))), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel(suffix)), []byte("#!/bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Only the baseline build is installed: the default-first order falls back to it.
	write("-baseline")
	if got := npmPlatformExecutable(root, "@opencode/cli", filepath.Join("bin", "opencode"), "", "-baseline"); got != rel("-baseline") {
		t.Fatalf("fallback picked %q", got)
	}
	// Production selection: with the default and baseline builds installed,
	// an x64 CPU without AVX2 (CPUID, on every OS) gets the baseline build.
	write("")
	want := rel("")
	if runtime.GOARCH == "amd64" && !cpu.X86.HasAVX2 {
		want = rel("-baseline")
	}
	if got := a.NPMExecutable(root); !muslLoader() && got != want {
		t.Fatalf("avx2=%v picked %q, want %q", cpu.X86.HasAVX2, got, want)
	}
	// With every build installed, the first suffix wins, never the default.
	for _, suffix := range []string{"-musl", "-baseline-musl"} {
		write(suffix)
	}
	for _, suffix := range []string{"-baseline", "-musl", "-baseline-musl", ""} {
		if got := npmPlatformExecutable(root, "@opencode/cli", filepath.Join("bin", "opencode"), suffix, ""); got != rel(suffix) {
			t.Fatalf("suffix %q picked %q", suffix, got)
		}
	}
}

// The order matches @opencode/cli 2.0.32 postinstall.mjs packageNames().
func TestOpenCodeVariants(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		avx2, musl   bool
		want         []string
	}{
		{"linux", "amd64", true, false, []string{"", "-baseline", "-musl", "-baseline-musl"}},
		{"linux", "amd64", false, false, []string{"-baseline", "", "-baseline-musl", "-musl"}},
		{"linux", "amd64", true, true, []string{"-musl", "-baseline-musl", "", "-baseline"}},
		{"linux", "amd64", false, true, []string{"-baseline-musl", "-musl", "-baseline", ""}},
		{"linux", "arm64", false, false, []string{"", "-musl"}},
		{"linux", "arm64", false, true, []string{"-musl", ""}},
		{"darwin", "amd64", false, false, []string{"-baseline", ""}},
		{"darwin", "amd64", true, false, []string{"", "-baseline"}},
		{"darwin", "arm64", false, false, []string{""}},
	} {
		if got := openCodeVariants(tc.goos, tc.goarch, tc.avx2, tc.musl); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s/%s avx2=%v musl=%v: got %q, want %q", tc.goos, tc.goarch, tc.avx2, tc.musl, got, tc.want)
		}
	}
}

// OpenCode passes each generation's skill directory in its config, so its
// generations must not claim the account-home skill limit.
func TestOpenCodeGenerationSkillsArePinned(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	g, err := materializeNative("opencode", home, dir, dir, "", harnessmgr.HarnessConfig{}, nil, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if reasons := strings.Join(g.PartialReasons, " "); strings.Contains(reasons, "skills are not pinned") {
		t.Fatalf("pinned OpenCode skills reported as unpinned: %s", reasons)
	}
	for _, c := range Capabilities() {
		a, err := Adapter(c.Harness)
		if err != nil || !c.MCP || c.SkillsPinned != a.SkillsPinned {
			t.Fatalf("capability %+v does not follow its adapter", c)
		}
	}
}
