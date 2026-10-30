package hooks

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

// TickRepositories lists the repositories a maintenance tick checks: each one
// with hooks, then each room checkout that hooks do not cover.
func (s Service) TickRepositories(ctx context.Context) ([]string, error) {
	repos, err := s.Repositories(ctx)
	checkouts, checkoutErr := s.roomCheckouts(ctx)
	seen := map[string]bool{}
	var out []string
	for _, repo := range append(repos, checkouts...) {
		if !seen[repo] {
			seen[repo] = true
			out = append(out, repo)
		}
	}
	return out, errors.Join(err, checkoutErr)
}

// roomCheckouts lists the repository and extra repositories of each room, once.
// A registered repository that left the disk is not listed.
func (s Service) roomCheckouts(ctx context.Context) ([]string, error) {
	if s.Checkouts != nil {
		return s.Checkouts(ctx)
	}
	rooms, err := room.DefaultStore().List(true)
	if err != nil {
		return nil, err
	}
	var paths []string
	seen := map[string]bool{}
	for _, rec := range rooms {
		for _, repo := range rec.Repos() {
			if !repo.Missing && !seen[repo.Path] {
				seen[repo.Path] = true
				paths = append(paths, repo.Path)
			}
		}
	}
	return paths, nil
}

// lastSkipMu guards lastSkip, the reason each room checkout last gave for not
// moving. An empty reason means the checkout was current or moved.
var (
	lastSkipMu sync.Mutex
	lastSkip   = map[string]string{}
)

// fastForwardRoomCheckout moves path to origin's default branch when path is a
// room checkout. The engine calls it right after path's fetch. A checkout it
// cannot move safely is skipped, and one log line says why, once per reason.
func (s Service) fastForwardRoomCheckout(ctx context.Context, path string) {
	paths, err := s.roomCheckouts(ctx)
	if err != nil {
		log.Printf("room checkouts: cannot list rooms: %v", err)
		return
	}
	// A room may reach its checkout through a symlink, so compare resolved paths.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		log.Printf("room checkout %s: cannot fast-forward: %v", path, err)
		return
	}
	isRoom := false
	for _, p := range paths {
		if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved == target {
			isRoom = true
			break
		}
	}
	if !isRoom {
		return
	}
	reason, err := fastForwardCheckout(ctx, path)
	if err != nil {
		log.Printf("room checkout %s: cannot fast-forward: %v", path, err)
		return
	}
	lastSkipMu.Lock()
	changed := lastSkip[target] != reason
	lastSkip[target] = reason
	lastSkipMu.Unlock()
	if changed && reason != "" {
		log.Printf("room checkout %s: skipped, %s", path, reason)
	}
}

// fastForwardCheckout fast-forwards dir to origin's default branch. It returns
// the reason it did not move dir, or an empty reason when dir is current or moved.
// It never resets, stashes or forces.
func fastForwardCheckout(ctx context.Context, dir string) (string, error) {
	branch, err := repoid.DefaultBranch(ctx, dir, repoid.Git)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(branch.Ref, "origin/") {
		return "origin/HEAD is not set", nil
	}
	current, err := repoid.Git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if current != branch.Name {
		return "it is on " + current + ", not " + branch.Name, nil
	}
	status, err := repoid.Git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "its tree has local changes", nil
	}
	ahead, err := repoid.Git(ctx, dir, "rev-list", "--count", branch.Ref+"..HEAD")
	if err != nil {
		return "", err
	}
	if ahead != "0" {
		return "it has local commits that " + branch.Ref + " lacks", nil
	}
	_, err = repoid.Git(ctx, dir, "merge", "--ff-only", "--quiet", branch.Ref)
	return "", err
}
