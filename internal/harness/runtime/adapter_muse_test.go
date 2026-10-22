package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestMuseAdapterPrivateNativeScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, err := Adapter("muse")
	if err != nil {
		t.Fatal(err)
	}
	if a.Harness != "muse" || a.Binary != "muse" || a.NPMPackage != "" || a.ConfigFormat != "json" || a.MCPKey != "mcpServers" {
		t.Fatalf("wrong Muse contract: %+v", a)
	}
	if a.ConfigFile != ".config/muse/settings.json" || a.SkillDir != ".config/muse/skills" {
		t.Fatalf("wrong native config scope: %q %q", a.ConfigFile, a.SkillDir)
	}
	if a.Exclusive {
		t.Error("Exclusive = true; an exclusive account lease queues every Muse turn behind the one running")
	}
	if a.Install == nil {
		t.Fatalf("Muse keeps a pinned installer: %+v", a)
	}
	// Every declared path stays relative to the account home or the real home.
	// No entry may mention HOME or XDG overrides.
	paths := append([]string{a.ConfigFile, a.SkillDir}, a.AuthFiles...)
	paths = append(paths, a.PersonalConfigFiles...)
	if !reflect.DeepEqual(a.AuthFiles, []string{".config/muse/auth.json"}) {
		t.Fatalf("wrong auth files: %v", a.AuthFiles)
	}
	if !reflect.DeepEqual(a.PersonalConfigFiles, []string{".config/muse/settings.json"}) {
		t.Fatalf("wrong personal config files: %v", a.PersonalConfigFiles)
	}
	for _, p := range paths {
		if p == "" || filepath.IsAbs(p) || p != filepath.Clean(p) || strings.Contains(p, "..") {
			t.Fatalf("path leaves the native scope: %q", p)
		}
		if strings.Contains(strings.ToUpper(p), "HOME") || strings.Contains(p, "XDG") {
			t.Fatalf("path overrides HOME or XDG: %q", p)
		}
	}
	// Preference import allowlist holds harmless display keys only. MCP,
	// skills, hooks, permissions, credentials, sandbox, model and provider
	// keys never import.
	forbidden := []string{"mcpServers", "mcp_servers", "skills", "hooks", "managed_hooks", "permissions", "provider", "model", "auth", "credential", "sandbox", "commands", "env", "tools"}
	for _, key := range a.ConfigAllowlist {
		if key != strings.ToLower(key) || !strings.Contains(key, ".") {
			t.Fatalf("allowlist key is not a dotted preference path: %q", key)
		}
		for _, root := range forbidden {
			if strings.HasPrefix(key, root+".") || key == root {
				t.Fatalf("allowlist imports a forbidden key: %q", key)
			}
		}
	}
	wantEnv := map[string]string{
		"MUSE_AUTH_PATH":         filepath.Join(home+".home", ".config", "muse", "auth.json"),
		"MUSE_NO_AUTO_UPDATE":    "1",
		"TBH_CREDENTIAL_BACKEND": "file",
		"META_API_KEY":           "",
	}
	got := a.NativeEnv(home)
	if !reflect.DeepEqual(got, wantEnv) {
		t.Fatalf("native env: got %v want %v", got, wantEnv)
	}
	for key, value := range got {
		if key == "HOME" || strings.HasPrefix(key, "XDG_") {
			t.Fatalf("native env overrides HOME or XDG: %q", key)
		}
		if key == "MUSE_AUTH_PATH" {
			if value != filepath.Join(home+".home", ".config", "muse", "auth.json") {
				t.Fatalf("native auth path disagrees with private XDG: %s", value)
			}
		} else if strings.Contains(value, home) {
			t.Fatalf("native env leaks the account home: %q", value)
		}
	}
	if a.AccountBlocked != "" {
		t.Fatalf("Muse must not be account-blocked: %q", a.AccountBlocked)
	}
	if args := a.ConfigArgs(home, "", a.SkillDir); len(args) != 0 {
		t.Fatalf("no generated settings file yields no pairs, got %v", args)
	}
	if args := a.LoginArgs("muse", home); !reflect.DeepEqual(args, []string{"muse", "login"}) {
		t.Fatalf("login args: %v", args)
	}

}

func TestMuseConfigArgsEncodeGeneratedSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, err := Adapter("muse")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	generated := filepath.Join(dir, "settings.json")
	doc := "{\n  \"schema_version\": 1,\n  \"mcpServers\": {\"web\": {\"url\": \"u\"}, \"docs\": {\"command\": \"python3\", \"args\": [\"x\"]}},\n  \"context\": {\"foreign_personal_skills\": false}\n}\n"
	if err := os.WriteFile(generated, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	// Root members lower to sorted one-run -c key=value pairs with compact
	// JSON values; skill files stay at SkillDir and need no pair.
	want := []string{
		"-c", `context={"foreign_personal_skills":false}`,
		"-c", `mcpServers.docs={"command":"python3","args":["x"]}`,
		"-c", `mcpServers.web={"url":"u"}`,
		"-c", "schema_version=1",
	}
	if args := a.ConfigArgs(home, generated, filepath.Join(dir, "skills")); !reflect.DeepEqual(args, want) {
		t.Fatalf("pairs:\n got %v\nwant %v", args, want)
	}
	if args := a.ConfigArgs(home, generated, ""); !reflect.DeepEqual(args, want) {
		t.Fatal("pair encoding is not deterministic")
	}
	for i := 1; i < len(want); i += 2 {
		key := strings.SplitN(want[i], "=", 2)[0]
		if key == "HOME" || strings.HasPrefix(key, "XDG_") {
			t.Fatalf("pair overrides HOME or XDG: %q", want[i])
		}
	}
	// An empty server map yields no pair; Muse refuses the whole map.
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"mcpServers":{},"schema_version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if args := a.ConfigArgs(home, empty, ""); !reflect.DeepEqual(args, []string{"-c", "schema_version=1"}) {
		t.Fatalf("empty server map pairs: %v", args)
	}
	// A missing or invalid generation yields no pairs and no invention.
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "absent.json"), broken} {
		if args := a.ConfigArgs(home, path, ""); args != nil {
			t.Fatalf("invalid generation produced pairs: %v", args)
		}
	}
}

const testMuseVersion = "1.4.10-R6427.1"

func TestMuseInstallFollowsReleaseManifest(t *testing.T) {
	platform := musePlatform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		t.Skip("no Muse artifact for this platform")
	}
	payload := []byte("muse-bin fixture")
	sum := sha256.Sum256(payload)
	official := museArtifactPrefix + "channel=muse&version=" + testMuseVersion + "&file=muse-bin"
	for _, tc := range []struct {
		name, version, algorithm, url string
		ok                            bool
	}{
		{"listed", testMuseVersion, "sha256", official, true},
		{"other version", "1.4.9-R6427.1", "sha256", official, false},
		{"other algorithm", testMuseVersion, "md5", official, false},
		{"foreign artifact", testMuseVersion, "sha256", "https://example.invalid/muse-bin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, _ := json.Marshal(map[string]any{"version": tc.version, "checksum_algorithm": tc.algorithm, "artifacts": map[string]any{
				platform: map[string]any{"url": tc.url, "checksum": hex.EncodeToString(sum[:]), "size": len(payload)},
			}})
			downloads := []string{}
			m := &BundleManager{Root: filepath.Join(t.TempDir(), "harnesses"), Run: func(_ context.Context, args, _ []string) error {
				source := args[len(args)-1]
				downloads = append(downloads, source)
				body := payload
				if source == museReleaseManifestURL(testMuseVersion) {
					body = manifest
				}
				return os.WriteFile(args[len(args)-2], body, 0600)
			}}
			got, err := installMuse(context.Background(), m, t.TempDir(), testMuseVersion, nil)
			if (err == nil) != tc.ok {
				t.Fatalf("got %+v, %v", got, err)
			}
			want := []string{museReleaseManifestURL(testMuseVersion)}
			if tc.ok {
				want = append(want, official)
			}
			if !reflect.DeepEqual(downloads, want) {
				t.Fatalf("downloads %v, want %v", downloads, want)
			}
		})
	}
}

func TestMuseInstallArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	staging := t.TempDir()
	payload := []byte("pinned muse-bin fixture")
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])
	source := "https://example.invalid/muse-bin-fixture"
	m := &BundleManager{Root: filepath.Join(t.TempDir(), "harnesses")}
	var seen []string
	m.Run = func(_ context.Context, args, env []string) error {
		seen = args
		if args[0] != "curl" || !reflect.DeepEqual(env, []string{"TEST=1"}) {
			t.Fatalf("installer argv must stay a plain vector: %v %v", args, env)
		}
		output := ""
		for i, arg := range args {
			if arg == "--output" && i+1 < len(args) {
				output = args[i+1]
			}
		}
		if output == "" {
			t.Fatalf("no output path in %v", args)
		}
		return os.WriteFile(output, payload, 0600)
	}
	got, err := installMuseArtifact(context.Background(), m, staging, []string{"TEST=1"}, testMuseVersion, source, checksum, int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	want := InstallArtifact{Executable: "muse-bin-" + testMuseVersion, Package: source, Integrity: "sha256-" + checksum}
	if got != want {
		t.Fatalf("artifact: got %+v want %+v", got, want)
	}
	if seen[len(seen)-1] != source || seen[len(seen)-2] == "" {
		t.Fatalf("download stays argv with the pinned source: %v", seen)
	}
	info, err := os.Stat(filepath.Join(staging, got.Executable))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("executable is not a regular file inside staging: %v %v", info, err)
	}
	body, err := os.ReadFile(filepath.Join(staging, got.Executable))
	if err != nil || !reflect.DeepEqual(body, payload) {
		t.Fatalf("staged bytes differ: %v", err)
	}
	// Nothing may be written to HOME.
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("install touched the home directory: %v %v", entries, err)
	}
}

func TestMuseInstallRejectsBadVersionsAndArtifacts(t *testing.T) {
	staging := t.TempDir()
	payload := []byte("wrong bytes")
	sum := sha256.Sum256(payload)
	checksum := hex.EncodeToString(sum[:])
	m := &BundleManager{Root: filepath.Join(t.TempDir(), "harnesses")}
	m.Run = func(_ context.Context, args, _ []string) error {
		output := ""
		for i, arg := range args {
			if arg == "--output" && i+1 < len(args) {
				output = args[i+1]
			}
		}
		return os.WriteFile(output, payload, 0600)
	}
	// Inexact versions, and versions without a readable release manifest,
	// do not install.
	for _, version := range []string{"1.2.3", "1.4.9-R6427.1", "1.4.10", "1.4.10-R6427.1-extra"} {
		if _, err := installMuse(context.Background(), m, staging, version, nil); err == nil {
			t.Fatalf("unverified version installed: %q", version)
		}
	}
	// A checksum or size mismatch stops the install.
	if _, err := installMuseArtifact(context.Background(), m, staging, nil, testMuseVersion, "https://example.invalid/muse-bin", checksum, 999); err == nil {
		t.Fatal("size mismatch installed")
	}
	if _, err := installMuseArtifact(context.Background(), m, staging, nil, testMuseVersion, "https://example.invalid/muse-bin", "0000000000000000000000000000000000000000000000000000000000000000", int64(len(payload))); err == nil {
		t.Fatal("checksum mismatch installed")
	}
	if entries, err := os.ReadDir(staging); err != nil || len(entries) != 0 {
		t.Fatalf("rejected artifacts remain in staging: %v %v", entries, err)
	}
}

func TestMuseGeneratedRunMembersAndRestoredArguments(t *testing.T) {
	r, home, id := generationFixture(t, "muse")
	fresh := buildGeneration(t, r, "muse", home, id)
	restored, err := loadNativeGeneration("muse", fresh.Directory, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{fresh.Args, restored.Args} {
		joined := strings.Join(args, " ")
		for _, want := range []string{`run.subagent_delegation_mode="off"`, `run.workflow_trigger_mode="off"`} {
			if !strings.Contains(joined, want) {
				t.Fatalf("missing Muse run control %s: %v", want, args)
			}
		}
		if strings.Contains(joined, "run=") {
			t.Fatalf("generated parent conflicts with per-turn instructions: %v", args)
		}
	}
	// A pinned generation with an older argument lowering must be rebuilt.
	raw, err := os.ReadFile(filepath.Join(fresh.Directory, nativeFeatureFile))
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	delete(old, "Version")
	raw, _ = json.MarshalIndent(old, "", "  ")
	if err := os.WriteFile(filepath.Join(fresh.Directory, nativeFeatureFile), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if currentNativeFeaturePolicy(fresh.Directory, "muse") {
		t.Fatal("reused old pinned arguments")
	}
}
