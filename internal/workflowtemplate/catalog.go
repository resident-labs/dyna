// Package workflowtemplate is the catalog of reusable workflow starter files.
//
// Templates are regular JavaScript files stored only in the user template
// registry (normally $XDG_CONFIG_HOME/dyna/templates). The filename is the
// stable identity. Listing, saving, showing, and copying never execute source
// and never inspect <work-dir>/examples or the process cwd. Copied files are
// ordinary scripts launched with dyna run <file>; the catalog is not a
// runtime name resolver.
//
// Install and update seed the shipped examples/ starters into that registry.
// A missing file is created. A file whose bytes still match the last seeded
// copy may be updated when the bundle changes. A user-modified file is left
// untouched. Writes are atomic.
package workflowtemplate

import (
	"bytes"
	"crypto/sha256"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"dyna-agent/internal/fsutil"
)

const (
	// MaxNameLength is the maximum accepted template identity length.
	MaxNameLength = 128
	// MaxSourceBytes is the exclusive upper bound for save and copy.
	MaxSourceBytes = 256 * 1024
	// TODO: legacy/shim remove later — names the previous release rollback manifest; remove with requirement 31.
	ManagedManifestName = ".managed.json"
)

// Source identifies which catalog tree a template came from.
type Source string

const (
	SourceUser Source = "user"
)

var (
	// ErrNotFound is returned when a name or path does not resolve to a regular file.
	ErrNotFound = errors.New("workflow template not found")
	// ErrExists is returned when a save or copy would overwrite without permission.
	ErrExists = errors.New("workflow template already exists")
	// ErrBusy means another command holds the registry source lock.
	ErrBusy = errors.New("template registry is busy")

	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

	jsMarkers = []string{
		"export", "return", "agent(", "phase(", "workflow(",
		"log(", "parallel(", "pipeline(",
	}
)

// Info is the discoverable metadata for one catalog entry.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      Source `json:"source"`
	Path        string `json:"path"`
}

// SaveResult is the JSON contract for a successful save.
type SaveResult struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Bytes       int    `json:"bytes"`
	Overwritten bool   `json:"overwritten"`
}

// ShowResult is the JSON contract for templates show.
type ShowResult struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      Source `json:"source"`
	Path        string `json:"path"`
	Content     string `json:"content"`
}

// CopyResult is the JSON contract for a successful copy into an editable file.
type CopyResult struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Overwritten bool   `json:"overwritten"`
}

// Catalog is one user-registry view of the named starter store.
type Catalog struct {
	RegistryDir string
}

// DefaultRegistryDir is the user-config template registry. On Linux this is
// normally $XDG_CONFIG_HOME/dyna/templates or ~/.config/dyna/templates.
func DefaultRegistryDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(dir, "dyna", "templates")
}

// New returns a catalog. An empty registryDir uses DefaultRegistryDir.
func New(registryDir string) Catalog {
	if strings.TrimSpace(registryDir) == "" {
		registryDir = DefaultRegistryDir()
	}
	return Catalog{RegistryDir: registryDir}
}

// NormalizeName validates a template identity. A single trailing ".js" is
// stripped; the filename remains the stable name.
func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".js")
	if name == "" {
		return "", fmt.Errorf("template name must not be empty")
	}
	if len(name) > MaxNameLength {
		return "", fmt.Errorf("template name exceeds %d characters", MaxNameLength)
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("unsafe template name %q", name)
	}
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("unsafe template name %q", name)
	}
	if name != filepath.Base(name) || filepath.Clean(name) != name {
		return "", fmt.Errorf("unsafe template name %q", name)
	}
	return name, nil
}

// ExtractMeta conservatively reads optional export const meta name/description
// string literals. It never executes source. The filename remains identity.
func ExtractMeta(src string) (name, description string) {
	metadata, err := plan.ReadMetadata(src)
	if err != nil {
		return "", ""
	}
	return metadata.Name, metadata.Description
}

// ValidateSource performs conservative checks without executing JavaScript.
func ValidateSource(src []byte) error {
	if len(src) == 0 {
		return fmt.Errorf("template source must not be empty")
	}
	if len(src) > MaxSourceBytes {
		return fmt.Errorf("template source exceeds %s", settings.FormatBytes(int64(MaxSourceBytes)))
	}
	if bytes.IndexByte(src, 0) >= 0 {
		return fmt.Errorf("template source must not contain NUL bytes")
	}
	if !utf8.Valid(src) {
		return fmt.Errorf("template source must be valid UTF-8")
	}
	text := string(src)
	for _, marker := range jsMarkers {
		if strings.Contains(text, marker) {
			return nil
		}
	}
	return fmt.Errorf("template source does not look like a Dyna workflow script")
}

// List returns regular JavaScript templates from the user registry, sorted
// by name. It never inspects examples/ or the process cwd.
func (c Catalog) List() ([]Info, error) {
	out, err := c.listDir(c.RegistryDir, SourceUser)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Info{}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func (c Catalog) listDir(dir string, source Source) ([]Info, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list %s templates: %w", source, err)
	}
	var out []Info
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".js" {
			continue
		}
		name, err := NormalizeName(strings.TrimSuffix(entry.Name(), ".js"))
		if err != nil || name+".js" != entry.Name() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := readRegularFile(path)
		if err != nil {
			if isSymlinkError(err) {
				continue
			}
			return nil, err
		}
		_, desc := ExtractMeta(string(data))
		out = append(out, Info{Name: name, Description: desc, Source: source, Path: path})
	}
	return out, nil
}

// ResolveName locates a safe template name in the user registry.
func (c Catalog) ResolveName(name string) (Info, string, error) {
	normalized, err := NormalizeName(name)
	if err != nil {
		return Info{}, "", err
	}
	if strings.TrimSpace(c.RegistryDir) == "" {
		return Info{}, "", fmt.Errorf("%w: %s", ErrNotFound, normalized)
	}
	path := filepath.Join(c.RegistryDir, normalized+".js")
	if !pathInsideDir(c.RegistryDir, path) {
		return Info{}, "", fmt.Errorf("%w: %s", ErrNotFound, normalized)
	}
	data, err := readRegularFile(path)
	if err != nil {
		if os.IsNotExist(err) || isSymlinkError(err) {
			return Info{}, "", fmt.Errorf("%w: %s", ErrNotFound, normalized)
		}
		return Info{}, "", err
	}
	_, desc := ExtractMeta(string(data))
	return Info{Name: normalized, Description: desc, Source: SourceUser, Path: path}, string(data), nil
}

// Show returns metadata plus the unevaluated source for a named template.
func (c Catalog) Show(name string) (ShowResult, error) {
	info, src, err := c.ResolveName(name)
	if err != nil {
		return ShowResult{}, err
	}
	return ShowResult{
		Name: info.Name, Description: info.Description, Source: info.Source,
		Path: info.Path, Content: src,
	}, nil
}

// Save writes a user-registry starter atomically. Existing names collide
// unless overwrite is set.
func (c Catalog) Save(name string, src []byte, overwrite bool) (SaveResult, error) {
	normalized, err := NormalizeName(name)
	if err != nil {
		return SaveResult{}, err
	}
	if err := ValidateSource(src); err != nil {
		return SaveResult{}, err
	}
	if err := os.MkdirAll(c.RegistryDir, 0o755); err != nil {
		return SaveResult{}, err
	}
	path := filepath.Join(c.RegistryDir, normalized+".js")
	existed := false
	if info, err := os.Lstat(path); err == nil {
		existed = true
		if info.Mode()&os.ModeSymlink != 0 {
			return SaveResult{}, fmt.Errorf("refusing to write symlink %s", path)
		}
	} else if !os.IsNotExist(err) {
		return SaveResult{}, err
	}
	if err := writeAtomic(path, src, overwrite); err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Name: normalized, Path: path, Bytes: len(src), Overwritten: existed}, nil
}

// Copy materializes a resolved starter into dest, an ordinary editable .js file.
func (c Catalog) Copy(name, dest string, overwrite bool) (CopyResult, error) {
	info, src, err := c.ResolveName(name)
	if err != nil {
		return CopyResult{}, err
	}
	if len(src) > MaxSourceBytes {
		return CopyResult{}, fmt.Errorf("template source exceeds %s", settings.FormatBytes(int64(MaxSourceBytes)))
	}
	path, err := normalizeDest(dest)
	if err != nil {
		return CopyResult{}, err
	}
	existed := false
	if destInfo, err := os.Lstat(path); err == nil {
		if destInfo.Mode()&os.ModeSymlink != 0 {
			return CopyResult{}, fmt.Errorf("refusing to write symlink %s", path)
		}
		if !destInfo.Mode().IsRegular() {
			return CopyResult{}, fmt.Errorf("destination %s is not a regular file", path)
		}
		existed = true
		current, readErr := readRegularFile(path)
		if readErr == nil && bytes.Equal(current, []byte(src)) {
			return CopyResult{
				Name: info.Name, Source: info.Path, Destination: path, Overwritten: false,
			}, nil
		}
		if !overwrite {
			return CopyResult{}, fmt.Errorf("%w: %s (pass --overwrite)", ErrExists, path)
		}
	} else if !os.IsNotExist(err) {
		return CopyResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return CopyResult{}, err
	}
	if err := writeAtomic(path, []byte(src), overwrite || !existed); err != nil {
		return CopyResult{}, err
	}
	return CopyResult{
		Name: info.Name, Source: info.Path, Destination: path, Overwritten: existed,
	}, nil
}

func normalizeDest(dest string) (string, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return "", fmt.Errorf("destination path is required")
	}
	if strings.ContainsRune(dest, 0) {
		return "", fmt.Errorf("destination path must not contain NUL bytes")
	}
	if filepath.Ext(dest) != ".js" {
		return "", fmt.Errorf("destination must be a .js file")
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if filepath.Ext(abs) != ".js" {
		return "", fmt.Errorf("destination must be a .js file")
	}
	return abs, nil
}

func writeAtomic(path string, src []byte, overwrite bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return err
	}
	defer unlock()
	return replaceRegularFile(path, src, overwrite)
}

func replaceRegularFile(path string, src []byte, overwrite bool) error {
	info, err := os.Lstat(path)
	exists := err == nil
	if exists {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write symlink %s", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		if !overwrite {
			return fmt.Errorf("%w: %s", ErrExists, path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fsutil.WriteFile(path, src, 0o644, true)
}

func pathInsideDir(dir, path string) bool {
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	if dir == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func lockPathFor(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	root, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(root) == "" {
		root = os.TempDir()
	}
	lockRoot := filepath.Join(root, "dyna", "catalog-locks")
	if err := os.MkdirAll(lockRoot, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(lockRoot, hex.EncodeToString(sum[:])+".lock"), nil
}

func lockDir(dir string) (func(), error) {
	path, err := lockPathFor(dir)
	if err != nil {
		return nil, err
	}
	return store.Lock(path)
}

// tryLockDir keeps engine calls from waiting beyond their request deadline
// behind a source edit or a seed command from the previous release.
func tryLockDir(dir string) (func(), error) {
	path, err := lockPathFor(dir)
	if err != nil {
		return nil, err
	}
	unlock, owned, err := store.TryLock(path)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, fmt.Errorf("%w: %s; retry after the other command finishes", ErrBusy, dir)
	}
	return unlock, nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, symlinkError(path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxSourceBytes+1 {
		return nil, fmt.Errorf("template source exceeds %s", settings.FormatBytes(int64(MaxSourceBytes)))
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSourceBytes {
		return nil, fmt.Errorf("template source exceeds %s", settings.FormatBytes(int64(MaxSourceBytes)))
	}
	return data, nil
}

type symlinkRefError struct{ path string }

func (e symlinkRefError) Error() string { return e.path + " is a symlink" }

func symlinkError(path string) error { return symlinkRefError{path: path} }

func isSymlinkError(err error) bool {
	var se symlinkRefError
	return errors.As(err, &se)
}
