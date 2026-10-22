package runtime

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"dyna-agent/internal/account/native"
)

// These 1.3.0 pins match the official artifacts probed by T3 and Dyna.
type antigravityACPRelease struct {
	URL, SHA256 string
	Bytes       int64
}

var antigravityACPReleases = map[string]antigravityACPRelease{
	"linux-amd64":  {"https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-1.3.0-linux-x86_64.zip", "9fb60956af0a9d76220a4db91ca9ac88e2a2372ad68f985ab5fceace6b825b96", 333727150},
	"linux-arm64":  {"https://dl.google.com/agy-extensions/releases/linux/agy-acp-server-1.3.0-linux-arm64.zip", "500b0bc0fb858e88f4df404d4cedf80bf9298c178291e39e383d6c50b111cbdf", 321690363},
	"darwin-amd64": {"https://dl.google.com/agy-extensions/releases/macos/agy-acp-server-1.3.0-darwin-x86_64.zip", "bb23956b89984bf5d354af2c3725e6c57f0cc1b7228e77a0e91c9c2bc1d47646", 117245544},
	"darwin-arm64": {"https://dl.google.com/agy-extensions/releases/macos/agy-acp-server-1.3.0-darwin-arm64.zip", "7cd97045f7b4fe81175a107cdf16f9c51484e3c78a5162cae415338bb6aa5b88", 111456962},
}

// AntigravityACPExecutable locates the pinned server next to the CLI used for account management.
func AntigravityACPExecutable(cli string) string {
	return filepath.Join(filepath.Dir(cli), "agy_acp_server.par")
}

// AntigravityACPEnv selects the ACP server's private file storage, not the CLI token store.
func AntigravityACPEnv(cli, home string) map[string]string {
	env := native.AntigravityFileEnv()
	env["GEMINI_HOME"] = home
	env["AGY_ACP_FORCE_FILE_STORAGE"] = "1"
	env["ELECTRON_RUN_AS_NODE"] = "1"
	env["PYTHONUNBUFFERED"] = "1"
	env["ANTIGRAVITY_HARNESS_PATH"] = filepath.Join(filepath.Dir(cli), "localharness_external")
	env["TMPDIR"] = filepath.Join(home, "antigravity-acp", "tmp")
	// Clear alternate provider credentials so only this account's ACP login is used.
	for _, name := range []string{"GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "AGY_ACP_CCPA_PROJECT", "AGY_ACP_ENABLE_OAUTH"} {
		env[name] = ""
	}
	return env
}

// installAntigravitySessionRuntime retains the account CLI and adds its ACP companion.
// Both binaries belong to the same protected bundle.
func installAntigravitySessionRuntime(ctx context.Context, m *BundleManager, staging, version string, env []string) (InstallArtifact, error) {
	artifact, err := installAntigravity(ctx, m, staging, version, env)
	if err != nil {
		return InstallArtifact{}, err
	}
	pin, ok := antigravityACPReleases[runtime.GOOS+"-"+runtime.GOARCH]
	if !ok {
		return InstallArtifact{}, errors.New("Antigravity ACP has no verified artifact for this platform")
	}
	if err := installAntigravityACPArchive(ctx, m, staging, env, pin); err != nil {
		return InstallArtifact{}, err
	}
	// The CLI receipt remains the bundle version. Its companion has a fixed checked pin.
	artifact.Integrity += ";acp-sha256-" + pin.SHA256
	return artifact, nil
}

func installAntigravityACPArchive(ctx context.Context, m *BundleManager, staging string, env []string, pin antigravityACPRelease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(staging) || checkedDir(staging, false) != nil {
		return failure()
	}
	digest, err := hex.DecodeString(pin.SHA256)
	if err != nil || len(digest) != sha256.Size || pin.Bytes <= 0 {
		return failure()
	}
	file, err := os.CreateTemp(staging, ".antigravity-acp-*.zip")
	if err != nil {
		return failure()
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return failure()
	}
	defer os.Remove(path)
	args := []string{"curl", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--output", path, pin.URL}
	if err := RunBundleCommand(ctx, m, args, env); err != nil {
		return errors.New("Antigravity ACP artifact download failed")
	}
	file, err = os.Open(path)
	if err != nil {
		return failure()
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil || size != pin.Bytes || hex.EncodeToString(hash.Sum(nil)) != pin.SHA256 {
		return errors.New("Antigravity ACP artifact size or SHA-256 does not match its pin")
	}
	archive, err := zip.NewReader(file, size)
	if err != nil {
		return failure()
	}
	bin := filepath.Join(staging, "bin")
	if checkedDir(bin, true) != nil {
		return failure()
	}
	found := map[string]bool{}
	for _, member := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if member.Name != "agy_acp_server.par" && member.Name != "localharness_external" {
			continue
		}
		if found[member.Name] || !member.Mode().IsRegular() || member.UncompressedSize64 == 0 || member.UncompressedSize64 > 2<<30 {
			return errors.New("Antigravity ACP archive has an invalid executable member")
		}
		input, err := member.Open()
		if err != nil {
			return failure()
		}
		output, err := os.OpenFile(filepath.Join(bin, member.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
		if err != nil {
			input.Close()
			return failure()
		}
		_, copyErr := io.Copy(output, input)
		inputErr, outputErr := input.Close(), output.Close()
		if copyErr != nil || inputErr != nil || outputErr != nil {
			return failure()
		}
		found[member.Name] = true
	}
	if len(found) != 2 {
		return fmt.Errorf("Antigravity ACP archive must contain server and local harness")
	}
	return nil
}
