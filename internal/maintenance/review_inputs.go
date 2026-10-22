package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/pr/state"
)

// sweepReviewInputs removes reviewer input packs of PRs that closed or merged
// more than reviewInputRetention ago and whose newest file is that old too. A
// pack with no PR record goes when its newest file is that old. Packs of open
// PRs stay. With dry set it only measures.
func sweepReviewInputs(ctx context.Context, root string, now time.Time, dry bool) (Pruned, error) {
	entries, err := os.ReadDir(filepath.Join(root, "review-inputs"))
	if os.IsNotExist(err) {
		return Pruned{}, nil
	}
	if err != nil {
		return Pruned{}, err
	}
	st, err := state.Read(root)
	if err != nil {
		return Pruned{}, err
	}
	closedAt := map[string]time.Time{} // pack folder -> close time; zero while open
	for key, r := range st.Records {
		if r == nil {
			continue
		}
		at := time.Time{}
		if r.State == "closed" || r.State == "merged" {
			at = r.ClosedAt
		}
		closedAt[filepath.Base(state.ReviewInputsDir(root, key))] = at
	}
	var freed Pruned
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return freed, err
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, "review-inputs", entry.Name())
		bytes, newest, err := treeSize(ctx, dir)
		if err != nil {
			return freed, err
		}
		at, known := closedAt[entry.Name()]
		if known && at.IsZero() {
			continue // open PR, or closed without a recorded time
		}
		// The newest file also counts for a closed PR: a reopen keeps its
		// first ClosedAt, so a pack written since then starts a new period.
		if !known || newest.After(at) {
			at = newest
		}
		if now.Sub(at) <= reviewInputRetention {
			continue
		}
		if !dry {
			if err := os.RemoveAll(dir); err != nil {
				return freed, err
			}
		}
		freed.Items++
		freed.Bytes += bytes
	}
	return freed, nil
}
