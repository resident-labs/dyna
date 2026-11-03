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

// Per-room limits, so one session cannot fill the engine's disk with images.
const (
	MaxImagesPerRoom = 16
	MaxRoomBytes     = 128 << 20 // unpacked, over all of a room's images
)

// checkRecipe holds a Containerfile to the one rule: every stage starts FROM the base, declared by an ARG before the first
// FROM, so what a repository adds is layers on Dyna's image. Comments and blank lines are skipped and a trailing backslash
// joins lines, as a builder reads them. A later stage may start only from the base too, not from an earlier stage.
func checkRecipe(recipe, variable string) error {
	var instructions []string
	var joined string
	for _, line := range strings.Split(strings.ReplaceAll(string(recipe), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || (line == "" && joined == "") {
			continue
		}
		if part, more := strings.CutSuffix(line, "\\"); more {
			joined += part + " "
			continue
		}
		instructions = append(instructions, joined+line)
		joined = ""
	}
	declared, from := false, 0
	for _, instruction := range instructions {
		fields := strings.Fields(instruction)
		switch strings.ToUpper(fields[0]) {
		case "ARG":
			declared = declared || from == 0 && len(fields) == 2 && fields[1] == variable
		case "FROM":
			base := len(fields) >= 2 && (fields[1] == "${"+variable+"}" || fields[1] == "$"+variable)
			stage := len(fields) == 2 || len(fields) == 4 && strings.EqualFold(fields[2], "AS")
			if !base || !stage {
				return fmt.Errorf("every FROM must be FROM ${%s}, optionally AS a stage name: %q", variable, instruction)
			}
			from++
		default:
			if from == 0 {
				return fmt.Errorf("only ARG may come before the first FROM: %q", instruction)
			}
		}
	}
	if from == 0 || !declared {
		return fmt.Errorf("start with ARG %s, then FROM ${%s}", variable, variable)
	}
	return nil
}

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

// asidePrefix marks an image a save moved aside, and savePrefix a save in progress. Neither is a valid image name.
const (
	asidePrefix = ".old-"
	savePrefix  = ".save-"
)

// recoverSaves finishes what a crash left: a save in progress is dropped, and an image moved aside comes back unless a newer one
// took its place.
func recoverSaves(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if image, aside := strings.CutPrefix(entry.Name(), asidePrefix); aside {
			if _, err := os.Stat(filepath.Join(dir, image)); errors.Is(err, os.ErrNotExist) {
				if err := os.Rename(path, filepath.Join(dir, image)); err != nil {
					return err
				}
				continue
			}
		} else if !strings.HasPrefix(entry.Name(), savePrefix) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// Save puts a folder's archive in place of the room's image of that name. It checks the name, the Containerfile's base and
// box-needs first, and keeps the old image when any of it fails.
func (s *Store) Save(room contract.RoomID, name string, archive []byte) (contract.RoomImage, error) {
	if !validImageName.MatchString(name) {
		return contract.RoomImage{}, fmt.Errorf("%w: a name is lowercase letters, digits, _, . and -", ErrInvalid)
	}
	if len(archive) > skills.MaxPacked {
		return contract.RoomImage{}, fmt.Errorf("%w: the folder packs to %d KiB, over the limit of %d KiB", ErrInvalid, len(archive)>>10, skills.MaxPacked>>10)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.Layout.Images(room)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return contract.RoomImage{}, err
	}
	if err := recoverSaves(dir); err != nil {
		return contract.RoomImage{}, err
	}
	unpacked, err := os.MkdirTemp(dir, savePrefix)
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
	variable := "DYNA_BASE"
	if name == contract.CIRunnerImage {
		variable = "DYNA_CI_BASE"
	}
	if err := checkRecipe(string(recipe), variable); err != nil {
		return contract.RoomImage{}, fmt.Errorf("%w: %s of %s: %v", ErrInvalid, containerfile, name, err)
	}
	if needs, err := os.ReadFile(filepath.Join(unpacked, "box-needs")); err == nil {
		if _, err := ParseNeeds(string(needs)); err != nil {
			return contract.RoomImage{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	_, _, size, err := scan(unpacked)
	if err != nil {
		return contract.RoomImage{}, err
	}
	others, err := os.ReadDir(dir)
	if err != nil {
		return contract.RoomImage{}, err
	}
	count, total := 1, size
	for _, entry := range others {
		if !entry.IsDir() || !validImageName.MatchString(entry.Name()) || entry.Name() == name {
			continue
		}
		_, _, held, err := scan(filepath.Join(dir, entry.Name()))
		if err != nil {
			return contract.RoomImage{}, err
		}
		count++
		total += held
	}
	if count > MaxImagesPerRoom || total > MaxRoomBytes {
		return contract.RoomImage{}, fmt.Errorf("%w: a room holds at most %d images and %d MiB; remove one first", ErrInvalid, MaxImagesPerRoom, MaxRoomBytes>>20)
	}
	// The old image moves aside first, so a crash between the two renames leaves it for the next save to restore.
	target, aside := filepath.Join(dir, name), filepath.Join(dir, asidePrefix+name)
	if err := os.Rename(target, aside); err != nil && !errors.Is(err, os.ErrNotExist) {
		return contract.RoomImage{}, err
	}
	if err := os.Rename(unpacked, target); err != nil {
		return contract.RoomImage{}, err
	}
	if err := os.RemoveAll(aside); err != nil {
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
	if !validImageName.MatchString(name) {
		return contract.RoomImage{}, "", nil, ErrMissing
	}
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
