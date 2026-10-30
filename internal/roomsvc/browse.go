package roomsvc

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/filesvc"
	"dyna-agent/internal/room"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"time"
)

var ErrNotGit = errors.New("not a git repository")

// Path states for InspectPath.
const (
	PathRepo    = "repo"    // inside a git repository
	PathNotGit  = "notgit"  // an existing folder with no repository
	PathMissing = "missing" // the folder does not exist
)

// InspectPath reports what a typed path is: a repo, a plain folder or a
// missing folder. It never writes.
func (s *Service) InspectPath(path string) (PathCheck, error) {
	dir, err := filesvc.CleanTyped(path)
	if err != nil {
		return PathCheck{}, err
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		name, _ := room.RepoRoomName(dir)
		return PathCheck{State: PathMissing, Name: name}, nil
	case err != nil:
		return PathCheck{}, filesvc.ReadErr(err)
	case !info.IsDir():
		return PathCheck{}, filesvc.ErrBadPath
	}
	repo, err := room.RepoRoot(dir)
	if errors.Is(err, room.ErrNotRepo) {
		name, _ := room.RepoRoomName(dir)
		return PathCheck{State: PathNotGit, Name: name}, nil
	}
	if err != nil {
		return PathCheck{}, filesvc.ErrUnreadable
	}
	chk := PathCheck{State: PathRepo, Repo: repo}
	chk.Name, _ = room.RepoRoomName(repo)
	if rec, ok, _ := s.rooms().RepoRoom(repo); ok {
		chk.RoomID, chk.RoomName = rec.ID, rec.Name
	}
	return chk, nil
}

// CreateRoomInput is one New room request.
type CreateRoomInput struct {
	GitHubAccess room.GitHubAccess `json:"githubAccess"`
	Path         string            `json:"path"`
	// Name is the room name. Empty uses the repo name.
	Name string `json:"name"`
	// GitInit runs git init when the folder is not a repository. A missing
	// folder is created (mkdir -p) and then initialized.
	GitInit bool `json:"gitInit"`
	// CreatedBy is the record's creator. The engine sets it from the caller;
	// a request cannot.
	CreatedBy string `json:"-"`
}

// CreateRoom makes a room for the repo at in.Path, as `dyna room create
// --repo <path>` does. A repo that already has a room opens that room. With
// GitInit it also creates a missing folder and runs git init where needed.
func (s *Service) CreateRoom(ctx context.Context, in CreateRoomInput) (CreatedRoom, error) {
	if err := authority.Check("room create", authority.Of(ctx), nil); err != nil {
		return CreatedRoom{}, err
	}
	if in.GitHubAccess != "" {
		if err := room.ValidateGitHubAccess(in.GitHubAccess); err != nil {
			return CreatedRoom{}, err
		}
	}
	dir, err := filesvc.CleanTyped(in.Path)
	if err != nil {
		return CreatedRoom{}, err
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist) && in.GitInit:
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return CreatedRoom{}, errors.New("cannot create this folder")
		}
	case errors.Is(err, fs.ErrNotExist):
		return CreatedRoom{}, filesvc.ErrNoFolder
	case err != nil:
		return CreatedRoom{}, filesvc.ReadErr(err)
	case !info.IsDir():
		return CreatedRoom{}, filesvc.ErrBadPath
	}
	repo, err := room.RepoRoot(dir)
	if errors.Is(err, room.ErrNotRepo) {
		if !in.GitInit {
			return CreatedRoom{}, ErrNotGit
		}
		if err := gitInit(dir); err != nil {
			return CreatedRoom{}, err
		}
		repo, err = room.RepoRoot(dir)
	}
	if err != nil {
		return CreatedRoom{}, fmt.Errorf("cannot use this folder: %w", filesvc.ErrUnreadable)
	}
	// This lookup only supplies the UI status. Store.Create owns reuse and
	// revives an archived room under the creation lock.
	_, existed, err := s.rooms().RepoRoom(repo)
	if err != nil {
		return CreatedRoom{}, err
	}
	rec, err := s.CreateRoomRecord(ctx, room.CreateInput{Name: in.Name, Repo: repo, CreatedBy: in.CreatedBy, GitHubAccess: in.GitHubAccess})
	if err != nil {
		return CreatedRoom{}, err
	}
	return CreatedRoom{ID: rec.ID, Name: rec.Name, Repo: rec.Repo, Existed: existed}, nil
}

func gitInit(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", "init")
	cmd.Dir = dir
	if err := childenv.Clean(cmd).Run(); err != nil {
		return errors.New("git init failed")
	}
	return nil
}

// CreateRoomRecord creates or reuses a room from a resolved name and repository.
func (s *Service) CreateRoomRecord(ctx context.Context, in room.CreateInput) (room.Record, error) {
	if err := authority.Check("room create", authority.Of(ctx), nil); err != nil {
		return room.Record{}, err
	}
	return s.rooms().Create(in)
}
