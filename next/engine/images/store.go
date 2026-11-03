package images

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
)

var (
	// ErrInvalid is a save the store refuses: the folder or its name is not an image. The message says why.
	ErrInvalid = errors.New("invalid image")
	// ErrMissing says a room has no such image.
	ErrMissing = errors.New("no such image")
)

// containerfile is the file that makes a folder an image.
const containerfile = "Containerfile"

var validImageName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// fromLine finds a FROM naming a base variable, as ${DYNA_BASE}, $DYNA_BASE, or either with a stage name after.
func fromLine(variable string) *regexp.Regexp {
	return regexp.MustCompile(`(?mi)^\s*FROM\s+\$(\{` + variable + `\}|` + variable + `)(\s+AS\s+\S+)?\s*$`)
}

var (
	fromBase   = fromLine("DYNA_BASE")
	fromCIBase = fromLine("DYNA_CI_BASE")
)

// Store keeps each room's images in folders beside its instructions.
type Store struct {
	Layout box.Layout
	mu     sync.RWMutex
}

// scan reads an image folder: its content id, file count and size. The id hashes sorted paths, execute bits and contents.
func scan(dir string) (TreeID, int, int64, error) {
	hash := sha256.New()
	var files int
	var bytes int64
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%t\x00%d\x00", filepath.ToSlash(rel), info.Mode()&0o111 != 0, len(data))
		hash.Write(data)
		files++
		bytes += int64(len(data))
		return nil
	})
	return TreeID(fmt.Sprintf("%x", hash.Sum(nil))), files, bytes, err
}

func (s *Store) info(room contract.RoomID, name string) (contract.RoomImage, error) {
	dir := filepath.Join(s.Layout.Images(room), name)
	id, files, size, err := scan(dir)
	if errors.Is(err, os.ErrNotExist) {
		return contract.RoomImage{}, ErrMissing
	}
	return contract.RoomImage{Name: name, Room: room, ID: string(id), Files: files, Bytes: size}, err
}

// Save puts a folder's archive in place of the room's image of that name. It checks the name, the Containerfile's base and
// box-needs first, and keeps the old image when any of it fails.
func (s *Store) Save(room contract.RoomID, name string, archive []byte) (contract.RoomImage, error) {
	if !validImageName.MatchString(name) {
		return contract.RoomImage{}, fmt.Errorf("%w: a name is lowercase letters, digits, _, . and -", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.Layout.Images(room)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return contract.RoomImage{}, err
	}
	unpacked, err := os.MkdirTemp(dir, ".save-")
	if err != nil {
		return contract.RoomImage{}, err
	}
	defer os.RemoveAll(unpacked)
	if err := skills.Extract(archive, unpacked); err != nil {
		return contract.RoomImage{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	recipe, err := os.ReadFile(filepath.Join(unpacked, containerfile))
	if err != nil {
		return contract.RoomImage{}, fmt.Errorf("%w: the folder has no %s", ErrInvalid, containerfile)
	}
	want, variable := fromBase, "${DYNA_BASE}"
	if name == contract.CIRunnerImage {
		want, variable = fromCIBase, "${DYNA_CI_BASE}"
	}
	if !want.Match(recipe) {
		return contract.RoomImage{}, fmt.Errorf("%w: the %s of %s must have a line FROM %s", ErrInvalid, containerfile, name, variable)
	}
	if needs, err := os.ReadFile(filepath.Join(unpacked, "box-needs")); err == nil {
		if _, err := ParseNeeds(string(needs)); err != nil {
			return contract.RoomImage{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	target := filepath.Join(dir, name)
	if err := os.RemoveAll(target); err != nil {
		return contract.RoomImage{}, err
	}
	if err := os.Rename(unpacked, target); err != nil {
		return contract.RoomImage{}, err
	}
	return s.info(room, name)
}

// List gives a room's images by name.
func (s *Store) List(room contract.RoomID) ([]contract.RoomImage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.Layout.Images(room))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []contract.RoomImage
	for _, entry := range entries {
		if !entry.IsDir() || !validImageName.MatchString(entry.Name()) {
			continue // a save in progress
		}
		image, err := s.info(room, entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, image)
	}
	return out, nil
}

// Show gives one image with its Containerfile and its files' paths.
func (s *Store) Show(room contract.RoomID, name string) (contract.RoomImage, string, []string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	image, err := s.info(room, name)
	if err != nil {
		return image, "", nil, err
	}
	dir := filepath.Join(s.Layout.Images(room), name)
	recipe, err := os.ReadFile(filepath.Join(dir, containerfile))
	if err != nil {
		return image, "", nil, err
	}
	var files []string
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		files = append(files, filepath.ToSlash(rel))
		return err
	})
	slices.Sort(files)
	return image, strings.TrimSuffix(string(recipe), "\n"), files, err
}

// Remove deletes an image.
func (s *Store) Remove(room contract.RoomID, name string) error {
	if !validImageName.MatchString(name) {
		return ErrMissing
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Layout.Images(room), name)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return ErrMissing
	}
	return os.RemoveAll(dir)
}
