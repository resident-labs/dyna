package agent

import (
	"context"
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
	// diffTreeLimit bounds one worktree's section, and diffLimit the whole step. It is one try: nothing retries it.
	diffTreeLimit = 20 * time.Second
	diffLimit     = 60 * time.Second
)

// saveDiff writes everything the box's worktrees hold that is on no pushed ref to one file in folder: for each worktree
// the log of its unpushed commits and one diff, from the newest commit the branch shares with its remote branch (the base
// branch if it was never pushed) to a snapshot of the tree with its uncommitted and untracked files. The snapshot is made
// through a temporary index, so the branch and index do not change. Ignored files are not in it, and a symlink is a link
// and never followed. A worktree with nothing unpushed has no section, and with none the file is not written.
func saveDiff(ctx context.Context, root, folder string, session contract.SessionID, name string, repos []contract.BoxRepo, git gitRunner) (report contract.DiffSavedRequest) {
	ctx, cancel := context.WithTimeout(ctx, diffLimit)
	defer cancel()
	var out strings.Builder
	err := func() error {
		for _, repo := range repos {
			dir := filepath.Join(root, string(repo.Name))
			listed, err := git(ctx, dir, "", "worktree", "list", "--porcelain")
			if err != nil {
				return err
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
					against := ""
					for _, candidate := range []string{"origin/" + branch, "origin/" + repo.Base} {
						if _, err := git(ctx, tree, "", "rev-parse", "--verify", "--quiet", "refs/remotes/"+candidate); err == nil {
							against = candidate
							break
						}
					}
					if against == "" {
						return fmt.Errorf("%s has no remote branch to compare with", tree)
					}
					base, err := git(ctx, tree, "", "merge-base", head, "refs/remotes/"+against)
					if err != nil {
						return err
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
					count, err := git(ctx, tree, "", "rev-list", "--count", base+".."+head)
					if err != nil {
						return err
					}
					diff, err := git(ctx, tree, "", "diff", base, snapshot)
					if err != nil {
						return err
					}
					if count == "0" && diff == "" {
						return nil
					}
					log, err := git(ctx, tree, "", "log", base+".."+head)
					if err != nil {
						return err
					}
					names, err := git(ctx, tree, "", "diff", "--name-only", base, snapshot)
					if err != nil {
						return err
					}
					fmt.Sscan(count, &report.Commits)
					report.Files += len(strings.Fields(names))
					fmt.Fprintf(&out, "## %s %s\n# branch %s, against %s at %s\n# %s unpushed commits\n\n%s\n\n", repo.Name, tree, branch, against, base, count, log)
					if len(diff) > diffCap {
						stat, err := git(ctx, tree, "", "diff", "--stat=200", base, snapshot)
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
					return err
				}
			}
		}
		if out.Len() == 0 {
			return nil
		}
		if err := os.MkdirAll(folder, 0755); err != nil {
			return err
		}
		file := filepath.Join(folder, strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
				return r
			}
			return '_'
		}, name)+"-"+string(session)+".diff")
		tmp := file + ".tmp"
		if err := os.WriteFile(tmp, []byte(out.String()), 0644); err != nil {
			return err
		}
		if err := os.Rename(tmp, file); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		report.File = file
		return nil
	}()
	if err != nil {
		return contract.DiffSavedRequest{Error: err.Error()}
	}
	return report
}
