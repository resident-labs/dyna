package watch

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"dyna-agent/internal/hooks"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/worktrees"
)

var resolveCheckoutRepo = repoid.Resolve

// matchingCheckout compares the full remote identity, including its host.
func matchingCheckout(ctx context.Context, r Record, dir string) string {
	if dir == "" || changeKind(r) != "github" {
		return ""
	}
	host := r.Host
	if host == "" {
		host = "github.com"
	}
	want, err := repoid.RemoteIdentity("", "https://"+host+"/"+r.Repo)
	if err != nil {
		return ""
	}
	repo, err := resolveCheckoutRepo(ctx, dir)
	if err == nil && repo.ID == want {
		return repo.Root
	}
	return ""
}

func checkoutError(r Record) error {
	if changeKind(r) == "github" {
		return fmt.Errorf("repository checkout is unavailable; run dyna git pr track %s --repo <path>", refRecord(r).URL())
	}
	return fmt.Errorf("repository checkout is unavailable; run dyna git change open from the change's worktree")
}

// resolveCheckout repairs records created before checkout discovery was available.
// TODO: legacy/shim remove later — repairs persisted PR records with no checkout path.
func (s *Service) resolveCheckout(ctx context.Context, st State, r *Record) {
	if r.RepoPath != "" {
		return
	}
	cwd, _ := os.Getwd()
	if path := matchingCheckout(ctx, *r, cwd); path != "" {
		r.RepoPath = path
		return
	}
	known := []string{cwd, r.LookupDir, r.Dir}
	keys := make([]string, 0, len(st.Records))
	for key := range st.Records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		other := st.Records[key]
		known = append(known, other.RepoPath, other.LookupDir, other.Dir)
	}
	trees, _ := worktrees.List()
	for _, tree := range trees {
		known = append(known, tree.Path, tree.Repo)
	}
	mains, _ := hooks.New().Repositories(ctx)
	known = append(known, mains...)
	seen := map[string]bool{}
	// A known checkout can locate the head worktree even if that checkout has
	// another remote. Validate the selected worktree itself before using it.
	for _, dir := range known {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		list, err := repoid.Git(ctx, dir, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			continue
		}
		var path string
		for _, field := range strings.Split(list, "\x00") {
			if strings.HasPrefix(field, "worktree ") {
				path = strings.TrimPrefix(field, "worktree ")
			}
			if r.Branch != "" && field == "branch refs/heads/"+r.Branch {
				if match := matchingCheckout(ctx, *r, path); match != "" {
					r.RepoPath = match
					return
				}
			}
		}
	}
	for _, dir := range known {
		if path := matchingCheckout(ctx, *r, dir); path != "" {
			repo, err := repoid.Resolve(ctx, path)
			if err == nil {
				if main := matchingCheckout(ctx, *r, repo.Main); main != "" {
					r.RepoPath = main
					return
				}
			}
		}
	}
}
