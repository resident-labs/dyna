package filesvc

import (
	"bytes"
	"dyna-agent/internal/harness/runtime"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxWorkspaceBytes bounds file previews and unified diffs for quick remote reads.
const MaxWorkspaceBytes = 2 << 20

// WorkspaceRoot uses a room's repo or a session's actual working directory.
// Devices have the owner's shell access: absolute paths, parent paths and
// symlinks are intentional, and there is no secret-file deny list.
func (s *Service) WorkspaceRoot(roomRef, sessionRef string) (string, error) {
	rec, err := s.Rooms.Resolve(roomRef)
	if err != nil {
		return "", err
	}
	var root string
	if sessionRef != "" {
		info, err := s.SessionInRoom(rec.ID, sessionRef)
		if err != nil {
			return "", err
		}
		root = info.Dir
	} else {
		root = rec.Repo
	}
	if root == "" {
		return "", errors.New("this room has no repository directory")
	}
	return filepath.Abs(root)
}

// WorkspacePath resolves a path using the file browser's shell-access policy.
func WorkspacePath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

// ListEntries returns one directory level. Dot files are optional; .git
// entries are always omitted from listings, including worktree .git files.
func (s *Service) ListEntries(roomRef, sessionRef, path string, showHidden bool) (DirectoryEntries, error) {
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return DirectoryEntries{}, err
	}
	full := WorkspacePath(root, path)
	if runtime.IsAccountPath(full) {
		return DirectoryEntries{}, errors.New("private account files are not available in the file browser")
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return DirectoryEntries{}, err
	}
	out := DirectoryEntries{Root: root, Path: filepath.Clean(path), Entries: []DirectoryEntry{}}
	for _, e := range entries {
		if runtime.IsAccountPath(filepath.Join(full, e.Name())) {
			continue
		}
		if e.Name() == ".git" || (!showHidden && strings.HasPrefix(e.Name(), ".")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		} // a concurrent rename must not fail the whole directory
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		} else if e.Type()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		gitDir := false
		if kind == "dir" {
			_, err := os.Stat(filepath.Join(full, e.Name(), ".git"))
			gitDir = err == nil
		}
		out.Entries = append(out.Entries, DirectoryEntry{Name: e.Name(), Path: filepath.Join(path, e.Name()), Kind: kind, Size: info.Size(), ModTime: info.ModTime(), IsGit: gitDir})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if (a.Kind == "dir") != (b.Kind == "dir") {
			return a.Kind == "dir"
		}
		return a.Name < b.Name
	})
	return out, nil
}

// readWorkspaceBytes follows symlinks and refuses special files, whose reads
// can block forever. Size is capped while reading, including growing files.
func readWorkspaceBytes(path string) ([]byte, int64, bool, error) {
	if runtime.IsAccountPath(path) {
		return nil, 0, false, errors.New("private account files are not available in the file browser")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxWorkspaceBytes+1))
	if err != nil {
		return nil, 0, false, err
	}
	truncated := len(b) > MaxWorkspaceBytes
	if truncated {
		b = b[:MaxWorkspaceBytes]
	}
	return b, info.Size(), truncated, nil
}

// ReadFile returns text or an attachment-style image URL. Binary files have
// no text. Text previews may be truncated; image routes reject oversized files.
func (s *Service) ReadFile(roomRef, sessionRef, path string) (WorkspaceFile, error) {
	root, err := s.WorkspaceRoot(roomRef, sessionRef)
	if err != nil {
		return WorkspaceFile{}, err
	}
	b, size, truncated, err := readWorkspaceBytes(WorkspacePath(root, path))
	if err != nil {
		return WorkspaceFile{}, err
	}

	out, mime := PreviewWorkspaceFile(path, b, size, truncated)
	if strings.HasPrefix(mime, "image/") {
		rec, err := s.Rooms.Resolve(roomRef)
		if err != nil {
			return WorkspaceFile{}, err
		}
		out.URL = "/attachments/" + rec.ID + "/workspace?" + url.Values{"session": {sessionRef}, "path": {path}}.Encode()
	}
	return out, nil
}

// PreviewWorkspaceFile shapes current and historical bytes consistently. The
// second result is the safe content type used by the image preview transport.
func PreviewWorkspaceFile(path string, b []byte, size int64, truncated bool) (WorkspaceFile, string) {
	out := WorkspaceFile{Path: filepath.Clean(path), Name: filepath.Base(path), Size: size, Language: fileLanguage(path), Truncated: truncated}
	// Remove only an incomplete UTF-8 tail caused by the preview boundary.
	if truncated {
		for n := 0; n < 3 && len(b) > 0 && !utf8.Valid(b) && b[len(b)-1] >= 0x80; n++ {
			b = b[:len(b)-1]
		}
	}
	out.Binary = bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b)
	mime := fileType(b)
	if strings.HasPrefix(mime, "image/") {
		out.Binary = true
	} else if !out.Binary {
		out.Text = string(b)
	}
	return out, mime
}

func fileLanguage(path string) string {
	switch filepath.Base(path) {
	case "Dockerfile":
		return "dockerfile"
	case "Makefile", "GNUmakefile":
		return "makefile"
	}
	ext := strings.ToLower(filepath.Ext(path))
	languages := map[string]string{
		".go": "go", ".js": "javascript", ".mjs": "javascript", ".cjs": "javascript",
		".jsx": "jsx", ".ts": "typescript", ".tsx": "tsx", ".svelte": "svelte",
		".json": "json", ".jsonc": "json", ".md": "markdown", ".mdx": "markdown",
		".py": "python", ".rb": "ruby", ".rs": "rust", ".java": "java", ".kt": "kotlin",
		".c": "c", ".h": "c", ".cpp": "cpp", ".hpp": "cpp", ".cs": "csharp",
		".sh": "bash", ".bash": "bash", ".zsh": "bash", ".fish": "fish",
		".html": "html", ".htm": "html", ".css": "css", ".scss": "scss",
		".xml": "xml", ".svg": "xml", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml",
		".sql": "sql", ".diff": "diff", ".patch": "diff", ".txt": "text", ".ini": "ini", ".conf": "text",
	}
	if lang, ok := languages[ext]; ok {
		return lang
	}
	return "text"
}
