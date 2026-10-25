package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// Kimi 2.3.1 ACP ignores --agent-file, but loads SYSTEM.md from its home.
// ${base_prompt} keeps Kimi's built-in prompt. The rendered system prompt is
// bound in profile.bind, outside the compacted message history, and replayed
// when ACP loads the session. Each instruction snapshot gets its own home
// view so concurrent sessions cannot replace one another's system prompt.
func kimiInstructionsHome(home, text string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", errors.New("Kimi Code system instructions need the private KIMI_CODE_HOME")
	}
	sum := sha256.Sum256([]byte(text))
	view := filepath.Join(home, ".dyna-system", hex.EncodeToString(sum[:12]))
	if err := os.MkdirAll(view, 0700); err != nil {
		return "", err
	}
	// Directory links keep credential refreshes and native session journals
	// in the original account, including sessions created before this view.
	for _, name := range []string{
		"config.toml", "credentials", "oauth", "device_id", "sessions",
		"session_index.jsonl", "file-history", "skills", "plugins", "agents",
	} {
		target := filepath.Join(home, name)
		link := filepath.Join(view, name)
		if err := os.Symlink(target, link); err != nil {
			if !os.IsExist(err) {
				return "", err
			}
			if existing, err := os.Readlink(link); err != nil || existing != target {
				return "", errors.New("Kimi Code instruction home has an unexpected account link")
			}
		}
	}
	if err := writeFileOnce(filepath.Join(view, "SYSTEM.md"), []byte("${base_prompt}\n\n"+text)); err != nil {
		return "", err
	}
	return view, nil
}
