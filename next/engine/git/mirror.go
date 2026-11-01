package git

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/compose"
	"github.com/resident-labs/dyna/engine/images"
)

// Runner executes git without putting credentials in command arguments.
type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

type MissingTokenError struct{}

func (MissingTokenError) Error() string { return "GH_TOKEN is not set" }

type GitRunner struct{}

func (GitRunner) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.Output()
}

type CacheHost interface {
	PushCache(context.Context, string, string, []string) error
}

// Mirror keeps no lock around reads. A clone appears whole, git updates refs atomically, and objects arrive before the
// refs that name them, so a read during a fetch sees the old state or the new one.
type Mirror struct {
	Layout  box.Layout
	Token   string
	Runner  Runner
	fetches shared // by repository
	pushes  shared // by repository and host
}

type cachePush struct {
	repo contract.RepoName
	host CacheHost
}

const SessionBranchPrefix = "v2/"

// Prepare optionally fetches and pushes branches before returning the box setup. Callers share fetches and pushes, but
// each result began after its caller asked and each push began after that caller's fetch ended, so no box starts on
// older refs than it would alone. The token is the engine's own, so sharing grants a caller nothing new.
func (m *Mirror) Prepare(ctx context.Context, host CacheHost, repo contract.RepoName, session contract.SessionID, fetch bool) (contract.BoxRepo, error) {
	if fetch && m.Token == "" {
		return contract.BoxRepo{}, MissingTokenError{}
	}
	path, err := m.Layout.Git(repo)
	if err != nil {
		return contract.BoxRepo{}, err
	}
	cache, err := m.Layout.HostGit(repo)
	if err != nil {
		return contract.BoxRepo{}, err
	}
	if fetch {
		err := m.fetches.do(ctx, repo, func(ctx context.Context) error {
			steps := box.NewStepLog("repo " + string(repo))
			defer steps.Done("fetch")
			return m.fetch(ctx, repo, path)
		})
		if err != nil {
			return contract.BoxRepo{}, err
		}
		err = m.pushes.do(ctx, cachePush{repo, host}, func(ctx context.Context) error {
			steps := box.NewStepLog("repo " + string(repo))
			defer steps.Done("cache push")
			return host.PushCache(ctx, path, cache, []string{"+refs/heads/*:refs/heads/*"})
		})
		if err != nil {
			return contract.BoxRepo{}, err
		}
	}
	out, err := m.Runner.Run(ctx, nil, "--git-dir", path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return contract.BoxRepo{}, err
	}
	return contract.BoxRepo{Name: repo, Cache: cache, Branch: SessionBranchPrefix + string(session), Base: strings.TrimSpace(string(out))}, nil
}

// fetch brings the mirror up to date with GitHub. A first clone goes to one side, so no reader finds a partial mirror
// and a failed clone leaves nothing behind.
func (m *Mirror) fetch(ctx context.Context, repo contract.RepoName, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	env := m.authEnv()
	remote := "https://github.com/" + string(repo) + ".git"
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		clone := path + ".clone"
		if err := os.RemoveAll(clone); err != nil {
			return err
		}
		if _, err := m.Runner.Run(ctx, env, "clone", "--bare", "--no-tags", remote, clone); err != nil {
			return err
		}
		return os.Rename(clone, path)
	}
	if err != nil {
		return err
	}
	if _, err := m.Runner.Run(ctx, env, "--git-dir", path, "fetch", "--prune", "--no-tags", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
		return err
	}
	out, err := m.Runner.Run(ctx, env, "--git-dir", path, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 || fields[0] != "ref:" || !strings.HasPrefix(fields[1], "refs/heads/") || fields[2] != "HEAD" {
		return errors.New("remote default branch not found")
	}
	_, err = m.Runner.Run(ctx, nil, "--git-dir", path, "symbolic-ref", "HEAD", fields[1])
	return err
}

func (m *Mirror) authEnv() []string {
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + m.Token))
	return []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + credential, "GIT_TERMINAL_PROMPT=0"}
}

// ImageContext reads a default-branch toolchain context from the mirror: the image called wanted, or the sole one when it
// is empty.
func (m *Mirror) ImageContext(ctx context.Context, repo contract.RepoName, wanted string) (string, images.TreeID, []byte, []box.Need, error) {
	path, err := m.Layout.Git(repo)
	if err != nil {
		return "", "", nil, nil, err
	}
	return images.Context(ctx, m.Runner, path, wanted)
}

// Size reads the repository's default box size from the mirror. A missing mirror has none.
func (m *Mirror) Size(ctx context.Context, repo contract.RepoName) (contract.BoxSizeName, error) {
	return m.ImageSize(ctx, repo, "")
}

// ImageSize reads the default box size of one image, or of the sole image when wanted is empty.
func (m *Mirror) ImageSize(ctx context.Context, repo contract.RepoName, wanted string) (contract.BoxSizeName, error) {
	path, err := m.Layout.Git(repo)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	}
	size, err := images.DefaultSize(ctx, m.Runner, path, wanted)
	return contract.BoxSizeName(size), err
}

// Cloned reports whether the repository has a mirror yet.
func (m *Mirror) Cloned(repo contract.RepoName) bool {
	path, err := m.Layout.Git(repo)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Refresh brings the mirror up to date, sharing a fetch with any box start of this repository.
func (m *Mirror) Refresh(ctx context.Context, repo contract.RepoName) error {
	if m.Token == "" {
		return MissingTokenError{}
	}
	path, err := m.Layout.Git(repo)
	if err != nil {
		return err
	}
	return m.fetches.do(ctx, repo, func(ctx context.Context) error { return m.fetch(ctx, repo, path) })
}

// ImageFolders lists the repository's image folders. A missing mirror has none.
func (m *Mirror) ImageFolders(ctx context.Context, repo contract.RepoName) ([]string, error) {
	path, err := m.Layout.Git(repo)
	if err != nil || !m.Cloned(repo) {
		return nil, err
	}
	return images.Folders(ctx, m.Runner, path)
}

// ComposeNames lists the repository's compose files.
func (m *Mirror) ComposeNames(ctx context.Context, repo contract.RepoName) ([]string, error) {
	path, err := m.Layout.Git(repo)
	if err != nil {
		return nil, err
	}
	blobs, err := images.ComposeBlobs(ctx, m.Runner, path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(blobs))
	for _, blob := range blobs {
		if compose.ValidName(blob.Name) {
			names = append(names, blob.Name)
		}
	}
	return names, nil
}

// ComposeFile reads one compose file, or nil when the repository has no such name. It reads a file over the limit only
// as far as the limit, since a repository chooses its files' sizes and the engine holds them in memory.
func (m *Mirror) ComposeFile(ctx context.Context, repo contract.RepoName, name string) ([]byte, error) {
	path, err := m.Layout.Git(repo)
	if err != nil || !m.Cloned(repo) {
		return nil, err
	}
	blobs, err := images.ComposeBlobs(ctx, m.Runner, path)
	if err != nil {
		return nil, err
	}
	for _, blob := range blobs {
		if blob.Name != name {
			continue
		}
		if blob.Bytes > compose.MaxFileBytes {
			return make([]byte, compose.MaxFileBytes+1), nil // Parse refuses this by its length
		}
		data, err := images.ReadBlob(ctx, m.Runner, path, blob.Object)
		if data == nil {
			data = []byte{}
		}
		return data, err
	}
	return nil, nil
}

// DeleteArchiveRef deletes only the recorded archive commit, never a changed ref.
func (m *Mirror) DeleteArchiveRef(ctx context.Context, repo contract.RepoName, ref, commit string) error {
	if m.Token == "" {
		return MissingTokenError{}
	}
	path, err := m.Layout.Git(repo)
	if err != nil {
		return err
	}
	remote := "https://github.com/" + string(repo) + ".git"
	out, err := m.Runner.Run(ctx, m.authEnv(), "ls-remote", remote, ref)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return nil
	}
	if len(fields) != 2 || fields[0] != commit || fields[1] != ref {
		return errors.New("archive ref changed")
	}
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if _, err := m.Runner.Run(ctx, nil, "init", "--bare", path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = m.Runner.Run(ctx, m.authEnv(), "--git-dir", path, "push", "--force-with-lease="+ref+":"+commit, remote, ":"+ref)
	return err
}

// Ref reads one remote reference without fetching its objects.
func (m *Mirror) Ref(ctx context.Context, repo contract.RepoName, ref string) (string, error) {
	out, err := m.Runner.Run(ctx, m.authEnv(), "ls-remote", "https://github.com/"+string(repo)+".git", ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 || fields[1] != ref {
		return "", errors.New("remote ref not found")
	}
	return fields[0], nil
}
