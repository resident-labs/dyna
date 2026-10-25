package worktrees

import "context"

// Orphaned lists trees kept by GC whose known session owners are all closed.
// It uses the cached default-branch reference and never fetches or removes trees.
func Orphaned(ctx context.Context, repo string) ([]Tree, error) {
	trees, err := Scan(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := make([]Tree, 0)
	for _, tree := range trees {
		if tree.Main {
			continue
		}
		owners := map[string]bool{}
		if tree.sessionID != "" {
			owners[tree.sessionID] = true
		}
		for _, owner := range tree.inferred.Sessions {
			owners[owner.ID] = true
		}
		if len(owners) == 0 {
			continue
		}
		closed := true
		for id := range owners {
			done, err := SessionClosed(id)
			if err != nil || !done {
				closed = false
				break
			}
		}
		if !closed {
			continue
		}
		tree = decide(tree, SessionClosed)
		if tree.Action == "kept" {
			out = append(out, tree)
		}
	}
	return out, nil
}
