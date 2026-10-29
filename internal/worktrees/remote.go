package worktrees

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/repoid"
)

// hasRemote reports whether the repository has the named remote, whose URL the
// authenticated fetch and push helpers need.
func hasRemote(ctx context.Context, dir, name string) bool {
	_, err := gitOutput(ctx, dir, "remote", "get-url", name)
	return err == nil
}

// resolveCommit resolves base to a full commit id in dir. An empty commit
// with an error means the repository cannot resolve base at all.
func resolveCommit(ctx context.Context, dir, base string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// remoteTrackingBase reports whether base names a remote-tracking ref such as
// origin/main, which the refresh fetch is meant to update.
func remoteTrackingBase(ctx context.Context, dir, base string) bool {
	out, err := gitOutput(ctx, dir, "rev-parse", "--symbolic-full-name", "--quiet", base)
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "refs/remotes/")
}

// baseRemote returns the configured remote that base names, such as upstream
// for upstream/main. A base that names no configured remote belongs to origin.
func baseRemote(ctx context.Context, dir, base string) string {
	out, err := gitOutput(ctx, dir, "remote")
	if err != nil {
		return "origin"
	}
	short := strings.TrimPrefix(base, "refs/remotes/")
	name := ""
	for _, candidate := range strings.Fields(string(out)) {
		if strings.HasPrefix(short, candidate+"/") && len(candidate) > len(name) {
			name = candidate
		}
	}
	if name == "" {
		return "origin"
	}
	return name
}

// pinBase resolves base to a commit id in the caller's repo before the
// worktree is created, and returns that id. A plain `git fetch` rewrites
// FETCH_HEAD (D-164) and moves remote-tracking refs, so a base that already
// names a commit is pinned as-is, a remote-tracking base is refreshed first
// from the remote it names, and a base the repository still cannot resolve is
// refused instead of being resolved differently by `git worktree add` later.
func pinBase(ctx context.Context, repo, main, base string) (string, error) {
	commit, resolveErr := resolveCommit(ctx, repo, base)
	if resolveErr == nil && !remoteTrackingBase(ctx, repo, base) {
		return commit, nil
	}
	// The fetch refreshes remote-tracking refs, and can still bring a commit
	// the caller named but the repository lacks.
	remote := baseRemote(ctx, repo, base)
	if hasRemote(ctx, main, remote) {
		if out, err := gitRemoteOutput(ctx, main, remote, "fetch", remote); err != nil {
			if resolveErr != nil {
				return "", fmt.Errorf("base %q does not name a commit in %s", base, repo)
			}
			return "", fmt.Errorf("fetch %s: %w: %s", remote, err, out)
		}
		commit, resolveErr = resolveCommit(ctx, repo, base)
	}
	if resolveErr != nil || commit == "" {
		return "", fmt.Errorf("base %q does not name a commit in %s", base, repo)
	}
	return commit, nil
}

// gitRemoteOutput gives GitHub HTTPS reads of the named remote the same account
// and repository checks as gh. Other hosts and transports keep their native
// credentials.
func gitRemoteOutput(ctx context.Context, dir, remoteName string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	remote, err := repoid.Git(ctx, dir, "remote", "get-url", remoteName)
	if err != nil {
		return nil, err
	}
	identity, err := repoid.RemoteIdentity(dir, remote)
	if err != nil {
		return nil, err
	}
	host, _, hosted := strings.Cut(identity, "/")
	if !hosted || host != "github.com" || !strings.HasPrefix(remote, "https://") {
		return gitOutput(ctx, dir, args...)
	}
	origin, err := url.Parse(remote)
	if err != nil {
		return nil, err
	}
	// Git matches credential paths case-sensitively. Keep the URL's path;
	// ghclient compares repository identities without regard to case.
	name := strings.TrimSuffix(strings.Trim(origin.Path, "/"), ".git")
	ctx = ghclient.WithRepository(ctx, host, name)
	cmd := gitexec.Command(ctx, dir, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = ghclient.RunGit(ctx, "", cmd)
	return out.Bytes(), err
}
