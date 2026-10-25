package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/repoid"
)

// maxInstructionsArg is the largest argv value Dyna passes; Linux rejects a
// single argument over 128 KB, and half of that leaves room for the rest.
const maxInstructionsArg = 64 * 1024

// nativeInstructions lists the harnesses whose CLI takes per-run instructions
// as a system-prompt addition. OpenCode uses the shared prompt path: its
// service loads ambient config at startup, not from each client environment.
// Grok and Antigravity over ACP have no per-launch flag, so they use the prompt path on
// a session's first turn.
var nativeInstructions = map[string]bool{
	profile.HarnessClaudeCode: true, profile.HarnessCodex: true, profile.HarnessPi: true,
	profile.HarnessMuse: true, profile.HarnessKimiCode: true,
}

// NativeInstructions reports whether harness takes Profile.RuntimeInstructions
// through its own channel on every run.
func NativeInstructions(harness string) bool { return nativeInstructions[harness] }

// WithPromptInstructions supplies a frozen snapshot for a harness without a
// per-run native channel. Reusing an already prepared prompt is idempotent.
func WithPromptInstructions(p profile.Profile, prompt string) string {
	if NativeInstructions(p.Harness) || p.RuntimeInstructions == "" {
		return prompt
	}
	prefix := p.RuntimeInstructions + "\n\n"
	if strings.HasPrefix(prompt, prefix) {
		return prompt
	}
	return prefix + prompt
}

// instructionsFile is the file that holds text. The directory name is the
// text's hash, so every call for the same text uses the same file.
func instructionsFile(text string) string {
	root, err := os.UserCacheDir()
	if err != nil {
		root = os.TempDir()
	}
	sum := sha256.Sum256([]byte(text))
	return filepath.Join(root, "dyna", "instructions", hex.EncodeToString(sum[:12]), "instructions.md")
}

// writeFileOnce writes data to path unless the file already holds it. Each
// writer uses its own temporary file and renames it into place, so runs with
// the same instructions can write at once and never see a partial file.
func writeFileOnce(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// instructionsNative prepares the native channel for RuntimeInstructions
// and returns any CLI args it needs:
//   - Claude Code: --append-system-prompt-file.
//   - Codex: -c developer_instructions (a TOML string).
//   - Pi: --append-system-prompt, which reads a file's contents.
//   - Muse: -c run.developer_prompt (a JSON string).
//   - Kimi Code: SYSTEM.md in a private home view (no CLI args).
//   - Grok and Antigravity over ACP: the first prompt of a session
//     carries them (WithPromptInstructions).
func instructionsNative(p profile.Profile) ([]string, error) {
	text := p.RuntimeInstructions
	if text == "" || !NativeInstructions(p.Harness) {
		return nil, nil
	}
	file := instructionsFile(text)
	writeFile := func() error { return writeFileOnce(file, []byte(text)) }
	var args []string
	switch p.Harness {
	case profile.HarnessClaudeCode, profile.HarnessPi:
		if err := writeFile(); err != nil {
			return nil, err
		}
		flag := "--append-system-prompt-file"
		if p.Harness == profile.HarnessPi {
			flag = "--append-system-prompt"
		}
		args = []string{flag, file}
	case profile.HarnessCodex:
		args = []string{"-c", "developer_instructions=" + quotedString(text)}
	case profile.HarnessMuse:
		args = []string{"-c", "run.developer_prompt=" + quotedString(text)}
	case profile.HarnessKimiCode:
		_, err := kimiInstructionsHome(p.Env["KIMI_CODE_HOME"], text)
		return nil, err

	}
	for _, a := range args {
		if len(a) > maxInstructionsArg {
			return nil, errors.New("instructions exceed the 64 KB argv limit for " + p.Harness)
		}
	}
	return args, nil
}

// quotedString is a JSON string. Codex parses -c values as TOML and Muse as
// JSON; a JSON string without HTML escaping is valid in both.
func quotedString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return string(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
}

// claudeRepositoryInstructions restores repository instructions while Claude's
// native project settings remain disabled. Discovery stops at the Git root.
func claudeRepositoryInstructions(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	root, err := repoid.Git(context.Background(), cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil
	} // A folder outside Git has no repository boundary.
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	withinRoot := func(path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if !withinRoot(dir) {
		return "", fmt.Errorf("working directory is outside repository root")
	}
	var parts []string
	total := 0
	for {
		for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
			path := filepath.Join(dir, name)
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return "", err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			if !withinRoot(resolved) {
				return "", fmt.Errorf("repository instructions escape Git root: %s", path)
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("repository instructions must be a regular file: %s", path)
			}
			file, err := os.Open(resolved)
			if err != nil {
				return "", err
			}
			fileInfo, statErr := file.Stat()
			if statErr != nil || !fileInfo.Mode().IsRegular() {
				file.Close()
				return "", fmt.Errorf("repository instructions must be a regular file: %s", path)
			}
			header := "Repository instructions from " + path + ":\n"
			remaining := maxInstructionsArg - total - len(header) - 2
			if remaining < 0 {
				file.Close()
				return "", fmt.Errorf("repository instructions exceed 64 KB")
			}
			content, readErr := io.ReadAll(io.LimitReader(file, int64(remaining)+1))
			closeErr := file.Close()
			if readErr != nil {
				return "", readErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			if len(content) > remaining {
				return "", fmt.Errorf("repository instructions exceed 64 KB")
			}
			part := header + string(content) + "\n\n"
			total += len(part)
			parts = append(parts, part)
			break
		}
		if dir == root {
			break
		}
		dir = filepath.Dir(dir)
	}
	slices.Reverse(parts)
	return strings.Join(parts, ""), nil
}
