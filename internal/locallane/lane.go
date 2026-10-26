// Package locallane selects repositories that publish tested local changes.
package locallane

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/settings"
)

const Integration = "dyna/integration"

func git(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := gitexec.Command(ctx, repo, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

type commonDirsKey struct{}

type commonDirs struct {
	mu   sync.Mutex
	dirs map[string]string
}

// WithCommonDirs lets one watcher tick read each checkout's common Git
// directory once. A checkout's common directory does not change in a tick.
func WithCommonDirs(ctx context.Context) context.Context {
	return context.WithValue(ctx, commonDirsKey{}, &commonDirs{dirs: map[string]string{}})
}

func commonDir(ctx context.Context, path string) (string, error) {
	cache, _ := ctx.Value(commonDirsKey{}).(*commonDirs)
	if cache != nil {
		cache.mu.Lock()
		dir, ok := cache.dirs[path]
		cache.mu.Unlock()
		if ok {
			return dir, nil
		}
	}
	dir, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil && cache != nil {
		cache.mu.Lock()
		cache.dirs[path] = dir
		cache.mu.Unlock()
	}
	return dir, err
}

// Enabled compares common Git directories so linked worktrees use the same lane.
// A repository with no remote also uses the lane: nothing else can land its
// approved changes, because no PR can be opened and no CI service reads it.
func Enabled(ctx context.Context, repo string) (bool, error) {
	v, err := settings.Load("")
	if err != nil {
		return false, err
	}
	if len(v.LocalLaneRepos) > 0 {
		common, err := commonDir(ctx, repo)
		if err != nil {
			return false, err
		}
		for _, path := range v.LocalLaneRepos {
			candidate, err := commonDir(ctx, path)
			if err != nil {
				return false, err
			}
			if common == candidate {
				return true, nil
			}
		}
	}
	return NoRemote(ctx, repo), nil
}

// NoRemote reports a repository that has no remote at all.
func NoRemote(ctx context.Context, repo string) bool {
	out, err := git(ctx, repo, "remote")
	return err == nil && out == ""
}

// baseBranch picks the branch that integration starts from: origin/HEAD, then a
// local main or master. It never takes a checked-out branch, because that may be
// the change's own branch, which would put the change in integration before
// review. Otherwise the caller must supply a base.
func baseBranch(ctx context.Context, repo string) (string, error) {
	if remote, err := git(ctx, repo, "symbolic-ref", "--quiet", "--no-recurse", "refs/remotes/origin/HEAD"); err == nil {
		if name, ok := strings.CutPrefix(remote, "refs/remotes/origin/"); ok && name != "" {
			return "origin/" + name, nil
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name+"^{commit}"); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot tell which branch integration starts from; supply --base <ref>")
}

// Ensure creates integration once. The empty old value prevents concurrent reset.
func Ensure(ctx context.Context, repo string) (string, error) {
	ref := "refs/heads/" + Integration
	if head, err := git(ctx, repo, "rev-parse", "--verify", ref+"^{commit}"); err == nil {
		return head, nil
	}
	baseRef, err := baseBranch(ctx, repo)
	if err != nil {
		return "", err
	}
	base, err := git(ctx, repo, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return "", err
	}
	if _, err := git(ctx, repo, "update-ref", ref, base, ""); err != nil {
		// Another caller may have created integration while this caller read the default branch.
		if head, readErr := git(ctx, repo, "rev-parse", "--verify", ref+"^{commit}"); readErr == nil {
			return head, nil
		}
		return "", err
	}
	return base, nil
}
