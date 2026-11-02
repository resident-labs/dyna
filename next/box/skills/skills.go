// Package skills holds Dyna's built-in skills and what handles a skill's folder: its frontmatter, and the gzip tar
// that carries a folder from a box to the engine and from the engine to a host. A new built-in skill is a new folder here.
package skills

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"gopkg.in/yaml.v3"
)

const (
	// File is the file that makes a folder a skill.
	File = "SKILL.md"
	// ListFile is the name of a scope's list inside its bundle, beside one folder per skill.
	ListFile = "addons.json"
	// MaxPacked is what fits in one API request as base64, with room for the request around it.
	MaxPacked = 3 << 20
	// MaxBytes bounds what one archive holds unpacked.
	MaxBytes = 64 << 20
	maxFiles = 4096
)

//go:embed */*
var builtIn embed.FS

var validName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// List is one scope's add-ons as JSON, in the engine's file and in a bundle.
type List struct {
	Items []contract.Addon `json:"items"`
}

// Meta is what Pi lists of a skill.
type Meta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Parse reads a SKILL.md's frontmatter and refuses what Pi would not load.
func Parse(data []byte) (Meta, error) {
	var meta Meta
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\xef\xbb\xbf"), "\r\n", "\n")
	front, ok := strings.CutPrefix(text, "---\n")
	if ok {
		front, _, ok = strings.Cut(front, "\n---")
	}
	if !ok {
		return meta, errors.New("SKILL.md must start with frontmatter between --- lines")
	}
	if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
		return meta, fmt.Errorf("SKILL.md frontmatter: %w", err)
	}
	if len(meta.Name) > 64 || !validName.MatchString(meta.Name) {
		return meta, fmt.Errorf("skill name %q must be lowercase letters, digits and single hyphens, at most 64 characters", meta.Name)
	}
	if meta.Description = strings.TrimSpace(meta.Description); meta.Description == "" || len(meta.Description) > 1024 {
		return meta, errors.New("SKILL.md needs a description of at most 1024 characters")
	}
	return meta, nil
}

func builtInNames() []string {
	entries, err := fs.ReadDir(builtIn, ".")
	if err != nil {
		panic(err) // the embedded folder is fixed at build time
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// BuiltIn lists the skills every box gets.
func BuiltIn() []contract.Addon {
	var items []contract.Addon
	for _, name := range builtInNames() {
		data, err := fs.ReadFile(builtIn, name+"/"+File)
		if err != nil {
			panic(err)
		}
		meta, err := Parse(data)
		if err != nil {
			panic(fmt.Sprintf("built-in skill %s: %v", name, err))
		}
		items = append(items, contract.Addon{Kind: contract.AddonSkill, Name: meta.Name, Description: meta.Description, BuiltIn: true})
	}
	return items
}

// IsBuiltIn reports whether a built-in skill holds this name.
func IsBuiltIn(name string) bool { return slices.Contains(builtInNames(), name) }

// Install writes every built-in skill into dir.
func Install(dir string) error {
	return fs.WalkDir(builtIn, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(builtIn, path)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(dir, filepath.FromSlash(path)), 0o600, data)
	})
}

func writeFile(path string, mode fs.FileMode, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

// Archive builds a gzip tar. The same files in the same order give the same bytes, so a host's copy of one is not rewritten.
type Archive struct {
	out bytes.Buffer
	gz  *gzip.Writer
	tar *tar.Writer
}

func NewArchive() *Archive {
	a := &Archive{}
	a.gz = gzip.NewWriter(&a.out)
	a.tar = tar.NewWriter(a.gz)
	return a
}

// Add writes one file. A mode with an execute bit stays executable, so a skill's scripts still run.
func (a *Archive) Add(name string, mode fs.FileMode, data []byte) error {
	header := tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(data))}
	if mode&0o111 != 0 {
		header.Mode = 0o700
	}
	if err := a.tar.WriteHeader(&header); err != nil {
		return err
	}
	_, err := a.tar.Write(data)
	return err
}

// Folder adds the regular files under dir below prefix. A git folder is not part of a skill, and a link leads outside it.
func (a *Archive) Folder(prefix, dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return a.Add(filepath.ToSlash(filepath.Join(prefix, rel)), info.Mode(), data)
	})
}

// Bytes ends the archive.
func (a *Archive) Bytes() ([]byte, error) {
	if err := a.tar.Close(); err != nil {
		return nil, err
	}
	if err := a.gz.Close(); err != nil {
		return nil, err
	}
	return a.out.Bytes(), nil
}

// Extract unpacks an archive under dest. Every name must stay under dest, and the limits hold what a small archive can unpack to.
func Extract(data []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var total int64
	for count := 0; ; count++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if count >= maxFiles {
			return fmt.Errorf("more than %d files", maxFiles)
		}
		if !filepath.IsLocal(header.Name) {
			return fmt.Errorf("%q leaves the folder", header.Name)
		}
		if total += header.Size; total > MaxBytes {
			return fmt.Errorf("more than %d MiB", MaxBytes>>20)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o600)
		if header.Mode&0o111 != 0 {
			mode = 0o700
		}
		if err := writeFile(filepath.Join(dest, filepath.FromSlash(header.Name)), mode, content); err != nil {
			return err
		}
	}
}

// Inspect reads the SKILL.md out of an archive without unpacking it.
func Inspect(data []byte) (Meta, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return Meta{}, fmt.Errorf("skill archive: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return Meta{}, fmt.Errorf("no %s at the top of the folder", File)
		}
		if err != nil {
			return Meta{}, fmt.Errorf("skill archive: %w", err)
		}
		if header.Name == File {
			content, err := io.ReadAll(io.LimitReader(reader, 1<<20))
			if err != nil {
				return Meta{}, fmt.Errorf("skill archive: %w", err)
			}
			return Parse(content)
		}
	}
}

// Request turns what a caller named into a save request. A link goes to the engine as it is, and a local folder or
// SKILL.md travels inside the request, because the engine cannot read this machine. dir anchors a relative path.
func Request(path, dir string, room contract.RoomID, global bool) (contract.SkillSaveRequest, error) {
	req := contract.SkillSaveRequest{Room: room, Global: global}
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		req.URL = path
		return req, nil
	}
	if dir != "" && !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return req, err
	}
	archive := NewArchive()
	if info.IsDir() {
		if _, err := os.Stat(filepath.Join(path, File)); err != nil {
			return req, fmt.Errorf("%s has no %s", path, File)
		}
		err = archive.Folder("", path)
	} else {
		var data []byte
		if data, err = os.ReadFile(path); err == nil {
			err = archive.Add(File, 0o600, data)
		}
	}
	if err != nil {
		return req, err
	}
	if req.Archive, err = archive.Bytes(); err != nil {
		return req, err
	}
	if len(req.Archive) > MaxPacked {
		return req, fmt.Errorf("%s packs to %d KiB, over the limit of %d KiB", path, len(req.Archive)>>10, MaxPacked>>10)
	}
	return req, nil
}
