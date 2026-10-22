package runtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"dyna-agent/internal/account/native"
)

// antigravityManifestBaseURL hosts the official platform manifests. Each
// manifest names the latest version, artifact URL, and SHA-512.
const antigravityManifestBaseURL = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/"

func antigravityManifestURL(goos, goarch string, musl bool) (string, error) {
	if (goos != "linux" && goos != "darwin") || (goarch != "amd64" && goarch != "arm64") {
		return "", errors.New("Antigravity has no verified artifact for this platform")
	}
	platform := goos + "_" + goarch
	if goos == "linux" && musl {
		platform += "_musl"
	}
	return antigravityManifestBaseURL + platform + ".json", nil
}

// antigravityArtifactPrefix is the official artifact host and path. A manifest
// URL elsewhere is rejected.
const antigravityArtifactPrefix = "https://storage.googleapis.com/antigravity-public/antigravity-cli/"

type antigravityManifest struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA512  string `json:"sha512"`
}

func (m antigravityManifest) valid() bool {
	digest, err := hex.DecodeString(m.SHA512)
	return validVersion(m.Version) && strings.HasPrefix(m.URL, antigravityArtifactPrefix) && err == nil && len(digest) == sha512.Size
}

func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness: "antigravity", Binary: "agy", ConfigFormat: "json", MCPKey: "mcpServers", SkillsPinned: true,
		// The ACP server reads global skills from GEMINI_HOME/config/skills.
		// The shared view follows the exclusive account turn and its frozen generation.
		ConfigFile: "", SkillDir: "config/skills", ConfigAllowlist: []string{},
		// agy 1.4.2 loads ~/.gemini/GEMINI.md as global rules.
		AuthFiles: []string{native.AntigravityACPTokenFile, native.AntigravityAccountFile},
		// Sessions and sign-in run the pinned ACP server and its local harness.
		Requires: []string{"agy_acp_server.par", "localharness_external"},
		NativeEnv: func(string) map[string]string {
			env := native.AntigravityFileEnv()
			// The upstream troubleshooting guide documents the literal value true.
			env["AGY_CLI_DISABLE_AUTO_UPDATE"] = "true"
			return env
		},
		ConfigArgs: func(_ string, configPath, _ string) []string {
			return []string{"--add-dir", filepath.Dir(configPath)}
		},
		LoginArgs: func(executable, _ string) []string { return []string{executable} },
		PartialReasons: []string{
			"The official manifest offers only the latest version.",
		},
		// Antigravity renews its OAuth grant with no shared token lock
		// (docs/credential-refresh.md), so two agy runs in one home could race a
		// rotated refresh token. No live concurrent run was made to confirm it.
		Exclusive:       true,
		ExclusiveReason: "agy renews its OAuth grant without a shared token lock; concurrent runs in one home can race a rotated grant",
		Install:         installAntigravitySessionRuntime,
	})
}

func installAntigravity(ctx context.Context, m *BundleManager, staging, version string, env []string) (InstallArtifact, error) {
	source, err := antigravityManifestURL(runtime.GOOS, runtime.GOARCH, muslLoader())
	if err != nil {
		return InstallArtifact{}, err
	}
	var manifest antigravityManifest
	if err := downloadManifest(ctx, m, staging, env, source, &manifest); err != nil {
		return InstallArtifact{}, err
	}
	if manifest.Version != version {
		return InstallArtifact{}, fmt.Errorf("Antigravity publishes only its latest version, %s", manifest.Version)
	}
	if !manifest.valid() {
		return InstallArtifact{}, errors.New("Antigravity manifest does not name an official artifact")
	}
	return installAntigravityArchive(ctx, m, staging, env, manifest.URL, manifest.SHA512)
}

func installAntigravityArchive(ctx context.Context, m *BundleManager, staging string, env []string, source, digest string) (InstallArtifact, error) {
	if ctx.Err() != nil {
		return InstallArtifact{}, ctx.Err()
	}
	if !filepath.IsAbs(staging) || checkedDir(staging, false) != nil {
		return InstallArtifact{}, failure()
	}
	want, err := hex.DecodeString(digest)
	if err != nil || len(want) != sha512.Size {
		return InstallArtifact{}, failure()
	}
	payload, err := os.CreateTemp(staging, ".antigravity-*.tar.gz")
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
		return InstallArtifact{}, errors.New("Antigravity artifact download failed")
	}
	payload, err = os.Open(path)
	if err != nil {
		return InstallArtifact{}, failure()
	}
	defer payload.Close()
	hash := sha512.New()
	if _, err := io.Copy(hash, payload); err != nil || hex.EncodeToString(hash.Sum(nil)) != digest {
		return InstallArtifact{}, errors.New("Antigravity artifact SHA-512 does not match the selected official manifest")
	}
	if _, err := payload.Seek(0, io.SeekStart); err != nil {
		return InstallArtifact{}, failure()
	}
	compressed, err := gzip.NewReader(payload)
	if err != nil {
		return InstallArtifact{}, failure()
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	bin := filepath.Join(staging, "bin")
	if checkedDir(bin, true) != nil {
		return InstallArtifact{}, failure()
	}
	found := false
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return InstallArtifact{}, failure()
		}
		// Never extract paths, links, or other distribution members. Match the
		// official installer's single executable member exactly.
		if header.Name != "antigravity" {
			continue
		}
		if found || header.Typeflag != tar.TypeReg || header.Size <= 0 {
			return InstallArtifact{}, failure()
		}
		out, err := os.OpenFile(filepath.Join(bin, "agy"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if err != nil {
			return InstallArtifact{}, failure()
		}
		_, copyErr := io.Copy(out, archive)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			return InstallArtifact{}, failure()
		}
		found = true
	}
	if !found {
		return InstallArtifact{}, errors.New("Antigravity artifact has no regular antigravity executable")
	}
	return InstallArtifact{Executable: filepath.Join("bin", "agy"), Package: source, Integrity: "sha512-" + base64.StdEncoding.EncodeToString(want)}, nil
}
