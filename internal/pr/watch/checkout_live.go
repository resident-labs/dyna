package watch

import (
	"context"
	"fmt"
	"os"
)

// checkoutDir returns a checkout of the record's repository that is still on
// disk. A record keeps the worktree of the session that tracked it, and closing
// that session removes the worktree. Git then reports "fork/exec /usr/bin/git:
// no such file or directory" for the missing command directory, which names git
// instead of the folder that is gone (D-131).
//
// RepoPath is a checkout Dyna resolved and matched against the record's
// repository before storing it. LookupDir and Dir are only the directories the
// caller and its session worked in, so a GitHub record proves each of them with
// matchingCheckout before using it. The room repository outlives every session
// and is the last fallback, with the same proof.
func (s *Service) checkoutDir(ctx context.Context, r Record) (string, error) {
	if isDirectory(r.RepoPath) {
		return r.RepoPath, nil
	}
	for _, dir := range []string{r.LookupDir, r.Dir} {
		if !isDirectory(dir) {
			continue
		}
		if changeKind(r) == "github" && matchingCheckout(ctx, r, dir) == "" {
			continue
		}
		return dir, nil
	}
	if s.Notify == nil || s.Notify.Rooms == nil {
		return "", missingCheckoutError(r)
	}
	rec, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil || !isDirectory(rec.Repo) {
		return "", missingCheckoutError(r)
	}
	if changeKind(r) == "github" && matchingCheckout(ctx, r, rec.Repo) == "" {
		return "", fmt.Errorf("checkout %s for %s is not a directory any more, and the room repository %s is not %s; %w", storedCheckout(r), changeLabel(r), rec.Repo, r.Repo, checkoutError(r))
	}
	return rec.Repo, nil
}

// missingCheckoutError names the folder Dyna stored, so the failure points at
// the folder and not at git.
func missingCheckoutError(r Record) error {
	return fmt.Errorf("checkout %s for %s is not a directory any more; %w", storedCheckout(r), changeLabel(r), checkoutError(r))
}

func storedCheckout(r Record) string {
	for _, dir := range []string{r.RepoPath, r.LookupDir, r.Dir} {
		if dir != "" {
			return dir
		}
	}
	return "(none stored)"
}

func isDirectory(dir string) bool {
	st, err := os.Stat(dir)
	return dir != "" && err == nil && st.IsDir()
}
