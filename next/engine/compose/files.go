package compose

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const fileSuffix = ".yaml"

// Repo reads the repository level from the mirror on the control plane.
type Repo interface {
	// Cloned reports whether a mirror exists yet. A first box start makes it.
	Cloned(contract.RepoName) bool
	// Refresh brings the mirror up to date with GitHub.
	Refresh(context.Context, contract.RepoName) error
	ComposeNames(context.Context, contract.RepoName) ([]string, error)
	// ComposeFile returns nil for a name the repository does not have.
	ComposeFile(context.Context, contract.RepoName, string) ([]byte, error)
	ImageFolders(context.Context, contract.RepoName) ([]string, error)
}

// Files finds compose files. A name resolves global, then room, then repository, and the most specific file wins whole.
type Files struct {
	Layout box.Layout
	Repo   Repo
}

func localNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), fileSuffix)
		if ok && ValidName(name) && entry.Type().IsRegular() {
			names = append(names, name)
		}
	}
	return names, nil
}

func localFile(dir, name string) ([]byte, error) {
	file, err := os.Open(filepath.Join(dir, name+fileSuffix))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1)) // Parse refuses what passes the limit
	if data == nil {
		data = []byte{}
	}
	return data, err
}

// read returns the winning file and its level, or nil when no level has the name.
func (f *Files) read(ctx context.Context, room contract.RoomID, repo contract.RepoName, name string) ([]byte, contract.ComposeLevel, error) {
	if repo != "" {
		data, err := f.Repo.ComposeFile(ctx, repo, name)
		if data != nil || err != nil {
			return data, contract.ComposeRepo, err
		}
	}
	data, err := localFile(f.Layout.Compose(room), name)
	if data != nil || err != nil {
		return data, contract.ComposeRoom, err
	}
	data, err = localFile(f.Layout.Compose(""), name)
	return data, contract.ComposeGlobal, err
}

// parse checks a file and the images it builds against the repository as it stands.
func (f *Files) parse(ctx context.Context, repo contract.RepoName, data []byte) (*File, error) {
	file, err := Parse(data)
	if err != nil {
		return nil, err
	}
	builds := file.Builds()
	if repo == "" {
		if len(builds) > 0 {
			return nil, refuse("build needs a repository, and this session has none")
		}
		return file, nil
	}
	folders, err := f.Repo.ImageFolders(ctx, repo)
	if err != nil {
		return nil, err
	}
	for _, folder := range builds {
		if !slices.Contains(folders, folder) {
			return nil, refuse("%s has no image folder %s", repo, folder)
		}
	}
	if file.Agent.Build == "" && len(folders) > 1 {
		return nil, refuse("%s has several images, so the agent service must name one with build", repo)
	}
	return file, nil
}

// Load reads a file a session already chose. It re-checks the file every time, because the file can change.
func (f *Files) Load(ctx context.Context, room contract.RoomID, repo contract.RepoName, name string) (*File, error) {
	data, _, err := f.read(ctx, room, repo, name)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, refuse("no file named %s at any level", name)
	}
	return f.parse(ctx, repo, data)
}

// Start chooses the file for a new session. It returns the name to keep on the session, or "" when requested is empty and
// no default file exists, which starts the session with no compose file at all.
//
// A mirror is only as new as the last box start, and a repository's first session has none, so a name not found there may
// still be on GitHub. A named file is worth a fetch. A default is not, or every session of a repository with no compose
// files would wait for one, so it fetches only for a mirror that does not exist yet.
func (f *Files) Start(ctx context.Context, room contract.RoomID, repo contract.RepoName, requested string) (string, *File, error) {
	name := requested
	if name == "" {
		name = Default
	}
	if !ValidName(name) {
		return "", nil, refuse("%q is not a compose file name", name)
	}
	if repo != "" && !f.Repo.Cloned(repo) {
		if err := f.Repo.Refresh(ctx, repo); err != nil && requested != "" {
			return "", nil, err
		}
	}
	data, _, err := f.read(ctx, room, repo, name)
	if err != nil {
		return "", nil, err
	}
	if data == nil && repo != "" && requested != "" {
		if err := f.Repo.Refresh(ctx, repo); err != nil {
			return "", nil, err
		}
		if data, _, err = f.read(ctx, room, repo, name); err != nil {
			return "", nil, err
		}
	}
	if data == nil {
		if requested == "" {
			return "", nil, nil
		}
		return "", nil, refuse("no file named %s at any level; session compose_list shows what there is", name)
	}
	file, err := f.parse(ctx, repo, data)
	if err != nil {
		return "", nil, err
	}
	return name, file, nil
}

// Names lists every name a session in this room and repository can use.
func (f *Files) Names(ctx context.Context, room contract.RoomID, repo contract.RepoName) ([]string, error) {
	names := make(map[string]bool)
	for _, dir := range []string{f.Layout.Compose(""), f.Layout.Compose(room)} {
		local, err := localNames(dir)
		if err != nil {
			return nil, err
		}
		for _, name := range local {
			names[name] = true
		}
	}
	if repo != "" && f.Repo.Cloned(repo) {
		remote, err := f.Repo.ComposeNames(ctx, repo)
		if err != nil {
			return nil, err
		}
		for _, name := range remote {
			names[name] = true
		}
	}
	return slices.Sorted(maps.Keys(names)), nil
}

// List reports each name with its winning level, and why that file would be refused, if it would.
func (f *Files) List(ctx context.Context, room contract.RoomID, repo contract.RepoName) ([]contract.ComposeFile, error) {
	names, err := f.Names(ctx, room, repo)
	if err != nil {
		return nil, err
	}
	files := make([]contract.ComposeFile, 0, len(names))
	for _, name := range names {
		data, level, err := f.read(ctx, room, repo, name)
		if err != nil {
			return nil, err
		}
		entry := contract.ComposeFile{Name: name, Level: level}
		if _, err := f.parse(ctx, repo, data); err != nil {
			var refused box.StackError
			if !errors.As(err, &refused) {
				return nil, err
			}
			entry.Problem = refused.Reason
		}
		files = append(files, entry)
	}
	return files, nil
}
