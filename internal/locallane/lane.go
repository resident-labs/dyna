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
func Enabled(ctx context.Context, repo string) (bool, error) {
	v, err := settings.Load("")
	if err != nil {
		return false, err
	}
	if len(v.LocalLaneRepos) == 0 {
		return false, nil
	}
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
	return false, nil
}

// Ensure creates integration once. The empty old value prevents concurrent reset.
func Ensure(ctx context.Context, repo string) (string, error) {
	ref := "refs/heads/" + Integration
	if head, err := git(ctx, repo, "rev-parse", "--verify", ref+"^{commit}"); err == nil {
		return head, nil
	}
	base, err := git(ctx, repo, "rev-parse", "--verify", "refs/remotes/origin/main^{commit}")
	if err != nil {
		return "", err
	}
	if _, err := git(ctx, repo, "update-ref", ref, base, ""); err != nil {
		// Another caller may have created integration while this caller read main.
		if head, readErr := git(ctx, repo, "rev-parse", "--verify", ref+"^{commit}"); readErr == nil {
			return head, nil
		}
		return "", err
	}
	return base, nil
}
