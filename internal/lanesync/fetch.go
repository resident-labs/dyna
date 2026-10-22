package lanesync

import (
	"context"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/repoid"
	"os/exec"
	"strings"
	"time"
)

func FetchOrigin(ctx context.Context, repo string, run func(context.Context, *exec.Cmd) error) error {
	return fetchWithRunner(ctx, repo, "origin", run)
}
func fetch(ctx context.Context, repo, remote string) error {
	return fetchWithRunner(ctx, repo, remote, nil)
}
func fetchWithRunner(ctx context.Context, repo, remote string, run func(context.Context, *exec.Cmd) error) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := gitexec.Command(ctx, repo, "fetch", "--prune", remote)
	// The account lookup uses the checkout directory to find its room.
	cmd.Dir = repo
	url, e := read(ctx, repo, "remote", "get-url", remote)
	if e != nil {
		return e
	}
	identity, e := repoid.RemoteIdentity(repo, url)
	if e != nil {
		return e
	}
	roomRepository := remote == "origin"
	if !roomRepository {
		if originURL, err := read(ctx, repo, "remote", "get-url", "origin"); err == nil {
			originIdentity, err := repoid.RemoteIdentity(repo, originURL)
			roomRepository = err == nil && strings.EqualFold(identity, originIdentity)
		}
	}
	host, name, hosted := strings.Cut(identity, "/")
	if roomRepository && hosted && (host == "github.com" || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")) {
		// Use HTTPS so the room account also covers GitHub SSH remote URLs.
		if host == "github.com" {
			cmd = githubFetchCommand(ctx, repo, remote, url, identity)
			cmd.Dir = repo
		}
		ctx = ghclient.WithRepository(ctx, host, name)
		if run != nil {
			return run(ctx, cmd)
		}
		return ghclient.RunGit(ctx, "", cmd)
	}
	return cmd.Run()
}

// An insteadOf rule changes transport while retaining the named remote refspecs.
// remote.<name>.url is multivalued, so a command-scoped extra URL cannot replace it.
func githubFetchCommand(ctx context.Context, repo, remote, url, identity string) *exec.Cmd {
	return gitexec.Command(ctx, repo, "-c", "url.https://"+identity+".git.insteadOf="+url, "fetch", "--prune", remote)
}
