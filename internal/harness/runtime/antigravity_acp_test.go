package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
)

func TestAntigravityACPReleasePins(t *testing.T) {
	if len(antigravityACPReleases) != 4 {
		t.Fatalf("expected four verified Unix platforms, got %d", len(antigravityACPReleases))
	}
	if got := antigravityACPReleases["linux-amd64"]; got.SHA256 != "9fb60956af0a9d76220a4db91ca9ac88e2a2372ad68f985ab5fceace6b825b96" || got.Bytes != 333727150 {
		t.Fatalf("verified linux pin changed: %+v", got)
	}
	for platform, pin := range antigravityACPReleases {
		digest, err := hex.DecodeString(pin.SHA256)
		if err != nil || len(digest) != sha256.Size || pin.Bytes <= 0 || !strings.HasPrefix(pin.URL, "https://dl.google.com/agy-extensions/releases/") {
			t.Fatalf("invalid pin for %s", platform)
		}
	}
}

func antigravityACPZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, name := range names {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("fixture")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestAntigravityACPInstallExtractsOnlyVerifiedMembers(t *testing.T) {
	payload := antigravityACPZip(t, "../outside", "agy_acp_server.par", "localharness_external")
	sum := sha256.Sum256(payload)
	pin := antigravityACPRelease{URL: "https://dl.google.com/agy-extensions/releases/fixture.zip", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload))}
	stage := t.TempDir()
	m := &BundleManager{Run: func(_ context.Context, argv, env []string) error { return os.WriteFile(argv[10], payload, 0600) }}
	if err := installAntigravityACPArchive(t.Context(), m, stage, nil, pin); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agy_acp_server.par", "localharness_external"} {
		st, err := os.Stat(filepath.Join(stage, "bin", name))
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0700 {
			t.Fatalf("missing private executable %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(stage)
	if err != nil || len(entries) != 1 || entries[0].Name() != "bin" {
		t.Fatalf("unexpected install outputs: %v, %v", entries, err)
	}
}

func TestAntigravityACPInstallRejectsCorruptOrIncompleteArchive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []string
		corrupt bool
	}{
		{"checksum", []string{"agy_acp_server.par", "localharness_external"}, true},
		{"missing-harness", []string{"agy_acp_server.par"}, false},
		{"duplicate", []string{"agy_acp_server.par", "agy_acp_server.par", "localharness_external"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := antigravityACPZip(t, tc.members...)
			sum := sha256.Sum256(payload)
			pin := antigravityACPRelease{URL: "https://dl.google.com/agy-extensions/releases/fixture.zip", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(payload))}
			if tc.corrupt {
				payload[0] ^= 1
			}
			m := &BundleManager{Run: func(_ context.Context, argv, env []string) error { return os.WriteFile(argv[10], payload, 0600) }}
			if err := installAntigravityACPArchive(t.Context(), m, t.TempDir(), nil, pin); err == nil {
				t.Fatal("accepted invalid ACP archive")
			}
		})
	}
}

func TestAntigravityACPLaunchEnvironment(t *testing.T) {
	cli := "/private/bundle/bin/agy"
	home := "/private/account/.runtime.home"
	env := AntigravityACPEnv(cli, home)
	for key, want := range map[string]string{"GEMINI_HOME": home, "AGY_ACP_FORCE_FILE_STORAGE": "1", "ANTIGRAVITY_HARNESS_PATH": "/private/bundle/bin/localharness_external", "ELECTRON_RUN_AS_NODE": "1", "PYTHONUNBUFFERED": "1", "SSH_TTY": "dyna-private-file-store", "GEMINI_API_KEY": "", "AGY_ADC_AUTH": "", "ANTIGRAVITY_GEMINI_DIR": "", "JETSKI_APP_DATA_DIR": ""} {
		if env[key] != want {
			t.Errorf("%s=%q, want %q", key, env[key], want)
		}
	}
	if got := AntigravityACPExecutable(cli); got != "/private/bundle/bin/agy_acp_server.par" {
		t.Fatal(got)
	}
}

func TestAntigravityACPPublishesSkillsInNativeGeminiHome(t *testing.T) {
	r, home, id := generationFixture(t, "antigravity")
	addGenerationSkill(t, r, "antigravity")
	generation := buildGeneration(t, r, "antigravity", home, id)
	skill := filepath.Join(harnesshome.Home(home), "config", "skills", "base", "SKILL.md")
	data, err := os.ReadFile(skill)
	if err != nil || string(data) != "base" {
		t.Fatalf("ACP could not read captured skill: %q %v", data, err)
	}
	// The view uses the account's paired native HOME, never its credential scope.
	if _, err := os.Stat(filepath.Join(home, "config", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill view published into credential scope: %v", err)
	}
	if generation.Directory == "" {
		t.Fatal("generation was not published")
	}
}
