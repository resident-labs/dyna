package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const (
	// diffFolder is where a closing box leaves what it has not pushed, in the room's shared folder.
	diffFolder = "/box/shared/archives"
	// diffCap is the largest diff one section carries. Over it the section holds the commit list and `git diff --stat`.
	diffCap = 10 << 20
	// emptyTree is the tree of nothing: a branch with no remote branch to compare with is shown against it, whole.
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	// diffTreeLimit bounds one worktree's section, and diffLimit the whole step. It is one try: nothing retries it.
	diffTreeLimit = 20 * time.Second
	diffLimit     = 60 * time.Second
)

// diffFetchLimit bounds the fetch that brings a repository's remote branches up to date before its worktrees are compared with
// them. A variable so a test need not wait it out.
var diffFetchLimit = 15 * time.Second

// saveDiff writes everything the box's worktrees hold that is on no pushed ref to one file in folder: for each worktree
// the log of its unpushed commits and one diff, from the newest commit the branch shares with its remote branch (the base
// branch if it was never pushed; with neither, its whole history) to a snapshot of the tree with its uncommitted and
// untracked files, binaries included. The snapshot is made through a temporary index, so the branch and index do not change.
// Ignored files are not in it, and a symlink is a link and never followed. A worktree with nothing unpushed has no section.
// Before that each repository is fetched (pruning) within diffFetchLimit, so a commit the remote has counts as pushed even if the
// box's last fetch predates it; a fetch that fails or times out leaves the old refs and is reported as possibly stale figures.
// A worktree that fails gets a section saying why, and the others are kept. With no section the file is not written.
func saveDiff(ctx context.Context, root, folder string, session contract.SessionID, name string, repos []contract.BoxRepo, git gitRunner) (report contract.DiffSavedRequest) {
	ctx, cancel := context.WithTimeout(ctx, diffLimit)
	defer cancel()
	var out strings.Builder
	fail := func(tree string, err error) {
		report.Failed = append(report.Failed, fmt.Sprintf("%s: %v", tree, err))
		fmt.Fprintf(&out, "## %s\n# not saved: %v\n\n", tree, err)
	}
	for _, repo := range repos {
		dir := filepath.Join(root, string(repo.Name))
		// The remote branches are as old as the box's last fetch, so work merged since reads as unpushed. Fetch first, pruning the
		// branches the remote deleted, and on failure compare with what is there and say the figures may be stale.
		stale := ""
		fetchCtx, cancelFetch := context.WithTimeout(ctx, diffFetchLimit)
		_, err := git(fetchCtx, dir, "", "fetch", "--quiet", "--prune", "origin")
		cancelFetch()
		if err != nil {
			stale = fmt.Sprintf("# the remote could not be fetched, so these figures may be stale: %v\n", err)
			report.Stale = append(report.Stale, fmt.Sprintf("%s: %v", repo.Name, err))
		}
		listed, err := git(ctx, dir, "", "worktree", "list", "--porcelain")
		if err != nil {
			fail(dir, err)
			continue
		}
		for _, line := range strings.Split(listed, "\n") {
			tree, ok := strings.CutPrefix(line, "worktree ")
			if !ok {
				continue
			}
			if err := func() error {
				ctx, cancel := context.WithTimeout(ctx, diffTreeLimit)
				defer cancel()
				head, err := git(ctx, tree, "", "rev-parse", "HEAD")
				if err != nil {
					return err
				}
				branch, err := git(ctx, tree, "", "rev-parse", "--abbrev-ref", "HEAD")
				if err != nil {
					return err
				}
				against, base, unpushed := "", emptyTree, "HEAD"
				for _, candidate := range []string{"origin/" + branch, "origin/" + repo.Base} {
					if _, err := git(ctx, tree, "", "rev-parse", "--verify", "--quiet", "refs/remotes/"+candidate); err == nil {
						against = candidate
						break
					}
				}
				if against != "" {
					if base, err = git(ctx, tree, "", "merge-base", head, "refs/remotes/"+against); err != nil {
						return err
					}
					unpushed = base + ".." + head
				}
				f, err := os.CreateTemp("", "dyna-index-")
				if err != nil {
					return err
				}
				index := f.Name()
				defer os.Remove(index)
				if err := f.Close(); err != nil {
					return err
				}
				if err := os.Remove(index); err != nil {
					return err
				}
				if _, err := git(ctx, tree, index, "read-tree", "HEAD"); err != nil {
					return err
				}
				if _, err := git(ctx, tree, index, "add", "-A"); err != nil {
					return err
				}
				snapshot, err := git(ctx, tree, index, "write-tree")
				if err != nil {
					return err
				}
				count, err := git(ctx, tree, "", "rev-list", "--count", unpushed)
				if err != nil {
					return err
				}
				diff, err := git(ctx, tree, "", "diff", "--binary", "--no-renames", base, snapshot)
				if err != nil {
					return err
				}
				if count == "0" && diff == "" {
					return nil
				}
				log, err := git(ctx, tree, "", "log", unpushed)
				if err != nil {
					return err
				}
				names, err := git(ctx, tree, "", "diff", "--name-only", "-z", "--no-renames", base, snapshot)
				if err != nil {
					return err
				}
				var n int
				fmt.Sscan(count, &n)
				report.Commits += n
				report.Files += len(strings.FieldsFunc(names, func(r rune) bool { return r == 0 }))
				where := "against " + against + " at " + base
				if against == "" {
					where = "with no remote branch to compare with, so its whole history"
				}
				fmt.Fprintf(&out, "## %s %s\n# branch %s, %s\n# %s unpushed commits\n%s\n%s\n\n", repo.Name, tree, branch, where, count, stale, log)
				if len(diff) > diffCap {
					stat, err := git(ctx, tree, "", "diff", "--stat=200", "--no-renames", base, snapshot)
					if err != nil {
						return err
					}
					report.Capped = true
					fmt.Fprintf(&out, "# the diff is over %d MiB, so this holds the commit list and `git diff --stat` only\n%s\n", diffCap>>20, stat)
					return nil
				}
				fmt.Fprintf(&out, "# diff\n%s\n", diff)
				return nil
			}(); err != nil {
				fail(tree, err)
			}
		}
	}
	if out.Len() == 0 {
		return report
	}
	if report.Commits == 0 && report.Files == 0 {
		return contract.DiffSavedRequest{Error: "nothing could be saved: " + strings.Join(report.Failed, "; ")}
	}
	write := func() error {
		if err := os.MkdirAll(folder, 0700); err != nil {
			return err
		}
		file := filepath.Join(folder, strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
				return r
			}
			return '_'
		}, name)+"-"+string(session)+".diff")
		// The file holds untracked files that are not ignored, so it asks for 0600 (the share mount ignores modes, spec 5.9), and the temporary file is made new: a planted
		// link there is removed, never followed.
		tmp := file + ".tmp"
		if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
			return err
		}
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(out.String())
		if err := errors.Join(err, f.Close()); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, file); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		report.File = file
		return nil
	}
	if err := write(); err != nil {
		return contract.DiffSavedRequest{Error: err.Error()}
	}
	return report
}
