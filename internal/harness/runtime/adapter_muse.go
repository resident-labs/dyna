package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	harnesshome "dyna-agent/internal/harness/home"
)

// Muse evidence (2026-10-17): no universal native config variable is verified.
// The native binary resolves auth.json under $XDG_CONFIG_HOME/muse, else
// $HOME/.config/muse; MUSE_AUTH_PATH is read by the launcher only (zero hits in
// muse-bin strings). Dyna binds both paths to private HOME/XDG roots.
// NativeEnv still sets MUSE_AUTH_PATH on purpose, so the launcher and sign-in
// resolve the same private auth file as the native binary.
// Sign-in uses the private Muse login behind the account/native harness gate. See
// https://meta-models.github.io/muse-code-sdk/next/guides/extend/skills/ and
// account/native/muse.go for the native paths.

// The official launcher reads the stable channel for the latest version, then
// that version's release manifest, which lists a sha256 checksum and size for
// each platform artifact. Dyna follows the same two steps.
const museChannelURL = "https://api.meta.ai/muse-code/channels/muse-stable"

func museReleaseManifestURL(version string) string {
	return "https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=" + url.QueryEscape(version) + "&file=manifest.json"
}

// museArtifactPrefix is the official download host and path. A manifest
// artifact URL elsewhere is rejected.
const museArtifactPrefix = "https://lookaside.facebook.com/lookaside/muse/download/?"

// musePlatform names the release-manifest artifact for a platform.
func musePlatform(goos, goarch string) string {
	return map[string]string{
		"linux/amd64": "x86_linux", "linux/arm64": "aarch64_linux",
		"darwin/amd64": "x86_macos", "darwin/arm64": "aarch64_macos",
	}[goos+"/"+goarch]
}

// The launcher's own version pattern: three numbers, then -R<number> with an
// optional .<number> suffix.
var museVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-R[0-9]+(\.[0-9]+)?$`)

func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness: "muse", Binary: "muse", ConfigFormat: "json", MCPKey: "mcpServers",
		// account/native/muse.go resolves native files under <home>/.config/muse.
		// The generated settings view keeps the root mcpServers map and the
		// skills.activation metadata the native settings JSON expects.
		ConfigFile: ".config/muse/settings.json", SkillDir: ".config/muse/skills",
		AuthFiles: []string{".config/muse/auth.json"},
		// These two TUI display keys are the only known
		// harmless personal preferences; everything else previews as unknown.
		ConfigAllowlist:     []string{"tui.verbose_output", "tui.foreign_context_notice_shown"},
		PersonalConfigFiles: []string{".config/muse/settings.json"},
		NativeEnv: func(home string) map[string]string {
			return map[string]string{
				"MUSE_AUTH_PATH":      filepath.Join(harnesshome.Roots(home)["XDG_CONFIG_HOME"], "muse", "auth.json"),
				"MUSE_NO_AUTO_UPDATE": "1",
				// account/native supports only the file credential backend; any
				// other value selects the shared operating-system keychain.
				"TBH_CREDENTIAL_BACKEND": "file",
				// "META_API_KEY overrides your Meta account"; clear the
				// inherited key so it cannot win over native credentials.
				"META_API_KEY": "",
			}
		},
		// No flag passes a settings or skill file. `-c/--config <KEY=VALUE>`
		// applies repeatable one-run raw pairs only, so ConfigArgs flattens the
		// generated root settings JSON into those pairs and skill files stay at
		// SkillDir.
		ConfigArgs: museConfigArgs,
		LoginArgs:  func(executable, _ string) []string { return []string{executable, "login"} },
		// Verified 2026-10-21 on Muse 1.4.10: a second `muse exec` in the
		// same private home answered while a long turn kept running there.
		// Each session has its own file and the index is SQLite. An
		// exclusive lease let one long workflow queue every other Muse turn
		// for hours, and a transfer gave up after 60 seconds of waiting.
		Install: installMuse,
	})
}

// museConfigArgs flattens the generated root settings JSON into one-run
// `-c key=value` pairs, the only settings channel Muse verifies. Each value is
// the compact JSON text of its root member and keys are sorted, so the argv is
// deterministic. MCP servers lower to one pair per server. A missing or invalid generation yields no pairs; main
// validates the generation before launch.
func museConfigArgs(_, configPath, _ string) []string {
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return nil
	}
	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]string, 0, 2*len(keys))
	for _, key := range keys {
		// Per-turn instructions and compaction use dotted paths. Lower these
		// namespaces by member so their parent cannot conflict with those paths.
		// Muse also requires one mcpServers.<name> pair per server.
		if key == "mcpServers" || key == "run" || key == "context_compaction" {
			members := map[string]json.RawMessage{}
			if json.Unmarshal(root[key], &members) != nil {
				return nil
			}
			names := make([]string, 0, len(members))
			for name := range members {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				pair, ok := museConfigPair(key+"."+name, members[name])
				if !ok {
					return nil
				}
				args = append(args, "-c", pair)
			}
			continue
		}
		pair, ok := museConfigPair(key, root[key])
		if !ok {
			return nil
		}
		args = append(args, "-c", pair)
	}
	return args
}

func museConfigPair(key string, raw json.RawMessage) (string, bool) {
	var value bytes.Buffer
	if json.Compact(&value, raw) != nil {
		return "", false
	}
	return key + "=" + value.String(), true
}

type museManifest struct {
	Version   string `json:"version"`
	Algorithm string `json:"checksum_algorithm"`
	Artifacts map[string]struct {
		URL      string `json:"url"`
		Checksum string `json:"checksum"`
		Size     int64  `json:"size"`
	} `json:"artifacts"`
}

func (m museManifest) supports(version, platform string) bool {
	a := m.Artifacts[platform]
	digest, err := hex.DecodeString(a.Checksum)
	return m.Version == version && m.Algorithm == "sha256" &&
		strings.HasPrefix(a.URL, museArtifactPrefix) && err == nil && len(digest) == sha256.Size && a.Size > 0
}

func installMuse(ctx context.Context, m *BundleManager, staging, version string, env []string) (InstallArtifact, error) {
	if !museVersionPattern.MatchString(version) {
		return InstallArtifact{}, errors.New("Muse requires an exact release version")
	}
	platform := musePlatform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		return InstallArtifact{}, errors.New("Muse bundle platform is unsupported")
	}
	var manifest museManifest
	if err := downloadManifest(ctx, m, staging, env, museReleaseManifestURL(version), &manifest); err != nil {
		return InstallArtifact{}, err
	}
	artifact := manifest.Artifacts[platform]
	if !manifest.supports(version, platform) {
		return InstallArtifact{}, errors.New("Muse release manifest does not list an official artifact for this version and platform")
	}
	return installMuseArtifact(ctx, m, staging, env, version, artifact.URL, artifact.Checksum, artifact.Size)
}

// installMuseArtifact retains one exact release-manifest artifact and verifies
// its published checksum and size. Nothing is written outside staging and no
// downloaded code runs during installation.
func installMuseArtifact(ctx context.Context, m *BundleManager, staging string, env []string, version, source, checksum string, size int64) (InstallArtifact, error) {
	if ctx.Err() != nil {
		return InstallArtifact{}, ctx.Err()
	}
	if !museVersionPattern.MatchString(version) {
		return InstallArtifact{}, failure()
	}
	want, err := hex.DecodeString(checksum)
	if err != nil || len(want) != sha256.Size || size <= 0 {
		return InstallArtifact{}, failure()
	}
	if err := checkedDir(staging, false); err != nil {
		return InstallArtifact{}, err
	}
	payload, err := os.CreateTemp(staging, ".muse-bin-*")
	if err != nil {
		return InstallArtifact{}, failure()
	}
	path := payload.Name()
	defer os.Remove(path)
	if payload.Close() != nil {
		return InstallArtifact{}, failure()
	}
	args := []string{"curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--output", path, source}
	if RunBundleCommand(ctx, m, args, env) != nil {
		return InstallArtifact{}, errors.New("Muse official artifact download failed")
	}
	file, err := os.Open(path)
	if err != nil {
		return InstallArtifact{}, failure()
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return InstallArtifact{}, failure()
	}
	if written != size {
		return InstallArtifact{}, errors.New("Muse artifact size does not match the selected official release manifest")
	}
	if hex.EncodeToString(hash.Sum(nil)) != checksum {
		return InstallArtifact{}, errors.New("Muse artifact checksum does not match the selected official release manifest")
	}
	if file.Close() != nil {
		return InstallArtifact{}, failure()
	}
	executable := "muse-bin-" + version
	if err := os.Chmod(path, 0755); err != nil {
		return InstallArtifact{}, failure()
	}
	if err := os.Rename(path, filepath.Join(staging, executable)); err != nil {
		return InstallArtifact{}, failure()
	}
	if _, err := executableHash(staging, executable); err != nil {
		return InstallArtifact{}, err
	}
	return InstallArtifact{Executable: executable, Package: source, Integrity: "sha256-" + hex.EncodeToString(want)}, nil
}
