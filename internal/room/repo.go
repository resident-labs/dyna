package room

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dyna-agent/internal/childenv"
)

// maxNameLen matches nameRe: one leading character and up to 62 more.
const maxNameLen = 63

// SanitizeName turns free text, such as a repo directory name, into a room
// name: lowercase, every run of characters outside [a-z0-9] becomes one -,
// no leading or trailing -, at most 63 bytes. It fails when nothing usable
// remains or the result is reserved.
func SanitizeName(raw string) (string, error) {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(raw) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > maxNameLen {
		name = strings.TrimRight(name[:maxNameLen], "-")
	}
	switch {
	case name == "":
		return "", fmt.Errorf("%q has no letters or digits to make a room name; pass an explicit name: dyna room create <name>", raw)
	case ReservedName(name) || ValidID(name):
		return "", fmt.Errorf("%q makes the reserved room name %q; pass an explicit name: dyna room create <name>", raw, name)
	}
	return name, ValidateName(name)
}

// ErrNotRepo reports a path outside any git repository.
var ErrNotRepo = errors.New("not inside a git repository")

// RepoRoot returns the canonical main worktree path of the git repo that
// holds path. Every linked worktree of one repo maps to the same main
// worktree, found through the shared git common dir.
func RepoRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	cmd := exec.Command("/usr/bin/git", "-C", abs, "rev-parse", "--path-format=absolute", "--git-common-dir")
	out, err := childenv.Clean(cmd).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("%s: %w", abs, ErrNotRepo)
		}
		return "", fmt.Errorf("run git: %w", err)
	}
	common := filepath.Clean(strings.TrimSpace(string(out)))
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		common = resolved
	}
	// A normal repo keeps its common dir at <main>/.git. A bare repo is its
	// own common dir, such as /srv/project.git.
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	return common, nil
}

// RepoRoomName derives the room name for the repo whose main worktree is
// root. A bare repo drops its .git suffix.
func RepoRoomName(root string) (string, error) {
	return SanitizeName(strings.TrimSuffix(filepath.Base(root), ".git"))
}

// RepoRoom finds the room whose repo is the main worktree root.
func (s *Store) RepoRoom(root string) (Record, bool, error) {
	records, err := s.List(true)
	if err != nil {
		return Record{}, false, err
	}
	for _, rec := range records {
		if rec.Repo == root {
			return rec, true, nil
		}
	}
	return Record{}, false, nil
}

// EnsureRepoRoom returns the room of the git repo that holds dir. Create
// reuses the repo's room, or makes one named after the repo.
func (s *Store) EnsureRepoRoom(dir, createdBy string) (Record, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return Record{}, err
	}
	return s.Create(CreateInput{Repo: root, CreatedBy: createdBy})
}
