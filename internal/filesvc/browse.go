package filesvc

import (
	"context"
	"dyna-agent/internal/harness/runtime"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits for the New room folder picker. They keep a listing fast.
const (
	MaxBrowsePath    = 512
	MaxBrowseEntries = 500
	BrowseTimeout    = 2 * time.Second
)

// Errors the picker shows.
var (
	ErrUnreadable = errors.New("cannot read this folder")
	ErrNoFolder   = errors.New("no such folder")
	ErrBrowseSlow = errors.New("this folder took too long to read")
	ErrBadPath    = errors.New("not a usable folder path")
)

// BrowseEntry is one folder in a listing. It carries no size, time or mode.
type BrowseEntry struct {
	Name  string `json:"name"`
	IsGit bool   `json:"isGit"`
}

// Browse is one level of folders.
type Browse struct {
	// Parent is the folder that was listed, with ~ expanded.
	Parent    string        `json:"parent"`
	Entries   []BrowseEntry `json:"entries"`
	Truncated bool          `json:"truncated,omitempty"`
}

// BrowseInfo tells the picker where to start.
type BrowseInfo struct {
	// Home is the user's home folder. The picker starts there and shows it
	// as ~/.
	Home string `json:"home"`
}

// BrowseInfo returns the home folder.
func (s *Service) BrowseInfo() (BrowseInfo, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return BrowseInfo{}, errors.New("cannot find the home folder")
	}
	return BrowseInfo{Home: home}, nil
}

// CleanTyped checks the typed text and expands ~. It returns a cleaned
// absolute path.
func CleanTyped(raw string) (string, error) {
	if len(raw) > MaxBrowsePath || strings.ContainsRune(raw, 0) || strings.TrimSpace(raw) == "" {
		return "", ErrBadPath
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ErrBadPath
		}
		raw = home + strings.TrimPrefix(raw, "~")
	}
	if !filepath.IsAbs(raw) {
		return "", ErrBadPath
	}
	return filepath.Clean(raw), nil
}

func isGitDir(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// ReadErr maps a file system error to the picker's wording.
func ReadErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return ErrUnreadable
	case errors.Is(err, fs.ErrNotExist):
		return ErrNoFolder
	}
	return ErrUnreadable
}

// BrowseDirs lists the folders directly under path. It lists one level and
// returns folders only, sorted by name. A symlink to a folder counts as a
// folder. Hidden folders are included: the client hides them.
func (s *Service) BrowseDirs(path string) (Browse, error) {
	dir, err := CleanTyped(path)
	if err != nil {
		return Browse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), BrowseTimeout)
	defer cancel()
	type result struct {
		out Browse
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := listDirs(ctx, dir)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-ctx.Done():
		return Browse{}, ErrBrowseSlow
	}
}

func listDirs(ctx context.Context, dir string) (Browse, error) {
	if runtime.IsAccountPath(dir) {
		return Browse{}, ErrUnreadable
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Browse{}, ErrNoFolder
		}
		return Browse{}, ReadErr(err)
	}
	out := Browse{Parent: dir, Entries: []BrowseEntry{}}
	var names []string
	for _, it := range items {
		if ctx.Err() != nil {
			return Browse{}, ErrBrowseSlow
		}
		name := it.Name()
		if name == ".git" || runtime.IsAccountPath(filepath.Join(dir, name)) {
			continue
		}
		isDir := it.IsDir()
		if it.Type()&fs.ModeSymlink != 0 {
			info, err := os.Stat(filepath.Join(dir, name))
			isDir = err == nil && info.IsDir()
		}
		if isDir {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := strings.ToLower(names[i]), strings.ToLower(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	if len(names) > MaxBrowseEntries {
		names, out.Truncated = names[:MaxBrowseEntries], true
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return Browse{}, ErrBrowseSlow
		}
		out.Entries = append(out.Entries, BrowseEntry{Name: name, IsGit: isGitDir(filepath.Join(dir, name))})
	}
	return out, nil
}
