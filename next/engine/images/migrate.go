package images

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
)

// migratedFile lists the repositories whose legacy images were moved into the room, one per line.
const migratedFile = ".migrated"

// migrateTimeout bounds the git calls of one repository's migration.
const migrateTimeout = 2 * time.Minute

// Migrated lists the repositories whose legacy images were moved into the room.
func (s *Store) Migrated(room contract.RoomID) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(filepath.Join(s.Layout.Images(room), migratedFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return strings.Fields(string(data)), err
}

// markMigrated records that a repository's legacy images are done: copied, or left for a reason that a retry would not change.
func (s *Store) markMigrated(room contract.RoomID, repo contract.RepoName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.Layout.Images(room)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(dir, migratedFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(string(repo) + "\n")
	return errors.Join(writeErr, file.Close())
}

// Migrate copies a repository's legacy images (.dyna/images on its default branch or on dyna-images) into the room. An image
// the room already has stays, the CI runner's name is skipped, an image over skills.MaxPacked of raw files is left, and a
// Containerfile the store refuses is left; these are final, and each is returned in left with its reason. A copied
// Containerfile is not rechecked: it has built so far, and the repository's lookup ends with this copy. Any other failure,
// such as a git call, is returned as err, so the caller does not record the repository and a later run tries again.
func (s *Store) Migrate(ctx context.Context, runner Runner, room contract.RoomID, mirror string) (copied, left []string, err error) {
	names, err := Folders(ctx, runner, mirror)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range names {
		// Another save can land between this check and the copy below; the copy then replaces it with the legacy image.
		held, err := s.List(room)
		if err != nil {
			return copied, left, err
		}
		if name == contract.CIRunnerImage || slices.ContainsFunc(held, func(i contract.RoomImage) bool { return i.Name == name }) {
			left = append(left, name+" (the room has one, or the name is reserved)")
			continue
		}
		_, tree, err := chooseImage(ctx, runner, mirror, name)
		if err != nil {
			return copied, left, err
		}
		listing, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", "-r", "-l", string(tree))
		if err != nil {
			return copied, left, err
		}
		var raw int64
		for _, line := range strings.Split(string(listing), "\n") {
			fields := strings.Fields(strings.SplitN(line, "\t", 2)[0])
			if len(fields) == 4 && fields[3] != "-" {
				size, err := strconv.ParseInt(fields[3], 10, 64)
				if err != nil {
					return copied, left, fmt.Errorf("read the size of %s: %w", name, err)
				}
				raw += size
			}
		}
		if raw > skills.MaxPacked {
			left = append(left, fmt.Sprintf("%s (%d KiB of files, over %d KiB)", name, raw>>10, skills.MaxPacked>>10))
			continue
		}
		_, _, tarball, _, err := Context(ctx, runner, mirror, name)
		if err != nil {
			return copied, left, err
		}
		var packed bytes.Buffer
		gz := gzip.NewWriter(&packed)
		_, writeErr := gz.Write(tarball)
		if err := errors.Join(writeErr, gz.Close()); err != nil {
			return copied, left, err
		}
		if _, err := s.save(room, name, packed.Bytes(), false); err != nil {
			if errors.Is(err, ErrInvalid) {
				left = append(left, name+" ("+err.Error()+")")
				continue
			}
			return copied, left, err
		}
		copied = append(copied, name)
	}
	return copied, left, nil
}

// Source is the repository mirrors, as a migration reads them.
type Source struct {
	Cloned  func(contract.RepoName) bool
	Refresh func(context.Context, contract.RepoName) error
	Dir     func(contract.RepoName) (string, error)
	Runner  Runner
}

// Migrator moves each room's legacy repository images into the room's store. Only the room's primary repository (or its sole
// repository) is moved, so a room never ends up with the images of several repositories, which would make every session
// start without a compose build refuse for the choice (ErrImageChoice). The names that other repositories hold are logged.
type Migrator struct {
	Store *Store
	Rooms interface {
		OpenRooms(context.Context) ([]contract.RoomID, error)
		RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error)
	}
	Source Source
	// Guard runs a write to the room's folder while no deletion can remove it. It is nil in tests.
	Guard func(context.Context, contract.RoomID, func() error) error
	mu    sync.Mutex
}

// All migrates every open room, as the engine does after it starts. A room that fails is logged and the rest go on.
func (m *Migrator) All(ctx context.Context) {
	rooms, err := m.Rooms.OpenRooms(ctx)
	if err != nil {
		log.Printf("migrate room images: list rooms: %v", err)
		return
	}
	for _, room := range rooms {
		m.Room(ctx, room, false)
	}
}

// Room migrates one room's primary repository once, and logs what it copied and left. With fetch a repository that has no
// mirror yet is fetched; without it the room waits for a later call. Every box start and scheduled run calls this first, so
// a mirror that came late, or a repository added after the upgrade, is moved before the room's image is chosen. A failure is
// logged and not recorded, so the next call retries it.
func (m *Migrator) Room(ctx context.Context, room contract.RoomID, fetch bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repos, err := m.Rooms.RoomRepos(ctx, room)
	if err != nil {
		log.Printf("room %s: migrate images: %v", room, err)
		return
	}
	var primary contract.RepoName
	for _, repo := range repos {
		if repo.Default || len(repos) == 1 {
			primary = repo.Repo
		}
	}
	if primary == "" {
		return
	}
	done, err := m.Store.Migrated(room)
	if err != nil {
		log.Printf("room %s: migrate images: %v", room, err)
		return
	}
	if slices.Contains(done, string(primary)) {
		return
	}
	if !m.Source.Cloned(primary) && fetch {
		if err := m.Source.Refresh(ctx, primary); err != nil {
			log.Printf("room %s: fetch %s for its images: %v", room, primary, err)
		}
	}
	if !m.Source.Cloned(primary) {
		log.Printf("room %s: the images of %s are not moved into the room yet: it has no mirror. Until then its sessions use the base image.", room, primary)
		return
	}
	dir, err := m.Source.Dir(primary)
	if err != nil {
		log.Printf("room %s: migrate images of %s: %v", room, primary, err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, migrateTimeout)
	defer cancel()
	var copied, left []string
	move := func() error {
		var err error
		if copied, left, err = m.Store.Migrate(ctx, m.Source.Runner, room, dir); err != nil {
			return err
		}
		return m.Store.markMigrated(room, primary)
	}
	if m.Guard != nil {
		err = m.Guard(ctx, room, move)
	} else {
		err = move()
	}
	if len(copied)+len(left) > 0 {
		log.Printf("room %s: moved the legacy .dyna/images of %s into the room: copied %v, left %v. Change an image with dyna image save.", room, primary, copied, left)
	}
	if err != nil {
		log.Printf("room %s: migrate images of %s: %v", room, primary, err)
		return
	}
	for _, repo := range repos {
		if repo.Repo == primary || !m.Source.Cloned(repo.Repo) {
			continue
		}
		other, err := m.Source.Dir(repo.Repo)
		if err != nil {
			continue
		}
		if names, err := Folders(ctx, m.Source.Runner, other); err == nil && len(names) > 0 {
			log.Printf("room %s: the images %v of %s are not moved: only the primary repository's are. Save one with dyna image save.", room, names, repo.Repo)
		}
	}
}
