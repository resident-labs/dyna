package hooks

import (
	"context"
	"log"
	"strings"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

// fastForwardRoomCheckouts moves each room checkout, the room's repository and
// its extra repositories, to origin's default branch. A checkout it cannot move
// safely is skipped, and one log line says why.
func fastForwardRoomCheckouts(ctx context.Context) {
	rooms, err := room.DefaultStore().List(true)
	if err != nil {
		log.Printf("room checkouts: cannot list rooms: %v", err)
		return
	}
	seen := map[string]bool{}
	for _, rec := range rooms {
		for _, repo := range rec.Repos() {
			if repo.Missing || seen[repo.Path] {
				continue
			}
			seen[repo.Path] = true
			reason, err := fastForwardCheckout(ctx, repo.Path)
			if err != nil {
				log.Printf("room checkout %s: cannot fast-forward: %v", repo.Path, err)
			} else if reason != "" {
				log.Printf("room checkout %s: skipped, %s", repo.Path, reason)
			}
		}
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
