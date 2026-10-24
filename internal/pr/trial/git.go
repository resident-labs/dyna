// Package trial builds and publishes the exact Git commits tested by merge CI.
package trial

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/ghclient"
)

var ErrMainMoved = errors.New("main moved outside the merge trial; rebuild the trial")

// Client requires the caller's room-scoped Git runner. It never writes into a
// user's checkout and removes its temporary repository after each operation.
type Client struct {
	Scratch string
	Run     func(context.Context, *exec.Cmd) error
}

type Batch struct {
	Remote, DefaultBranch, Base, Branch string
	Heads                               []string
	// Messages maps a head to its merge commit message. Heads without one
	// keep Git's default message.
	Messages map[string]string
	Created  time.Time
}

// MergeMessage names the PR in its trial merge commit and keeps its exact head.
func MergeMessage(number int, title, head string) string {
	title = strings.Join(strings.Fields(title), " ")
	if number <= 0 || title == "" {
		return ""
	}
	return fmt.Sprintf("Merge #%d: %s\n\nPR head: %s", number, title, head)
}

type Result struct {
	Head                string
	Included, Conflicts []string
}

var trialBranch = regexp.MustCompile(`^dyna/merge-trial/[1-9][0-9]*$`)
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (c Client) repository(ctx context.Context, batch Batch, fn func(string) error) error {
	return c.repositoryWithBranch(ctx, batch, true, fn)
}

func (c Client) repositoryWithBranch(ctx context.Context, batch Batch, requireTrialBranch bool, fn func(string) error) error {
	if c.Run == nil || c.Scratch == "" {
		return errors.New("merge trials require a scoped Git runner and scratch directory")
	}
	if requireTrialBranch && !trialBranch.MatchString(batch.Branch) || batch.Remote == "" || !commitID.MatchString(batch.Base) {
		return errors.New("invalid merge trial branch, remote, or base commit")
	}
	if err := os.MkdirAll(c.Scratch, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(c.Scratch, "merge-trial-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err = c.git(ctx, dir, batch.Created, "init", "--quiet"); err != nil {
		return err
	}
	if _, err = c.git(ctx, dir, batch.Created, "check-ref-format", "refs/heads/"+batch.DefaultBranch); err != nil {
		return err
	}
	return fn(dir)
}

func (c Client) git(ctx context.Context, dir string, at time.Time, args ...string) (string, error) {
	cmd := ghclient.Command(ctx, "git", dir, args...)
	// Trials must not inherit checkout filters, merge drivers, signing settings,
	// or hooks from the user's configuration. Authentication belongs to Run.
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=Dyna merge service", "GIT_AUTHOR_EMAIL=merge-trial@dyna.invalid",
		"GIT_COMMITTER_NAME=Dyna merge service", "GIT_COMMITTER_EMAIL=merge-trial@dyna.invalid")
	if !at.IsZero() {
		date := at.UTC().Format(time.RFC3339)
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	}
	out, stderr := boundedio.New(2<<20), boundedio.New(8192)
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := c.Run(ctx, cmd); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(stderr.Bytes())))
	}
	if out.Truncated() {
		return "", errors.New("Git output exceeded the merge-trial limit")
	}
	return strings.TrimSpace(string(out.Bytes())), nil
}

func (c Client) remoteHead(ctx context.Context, dir string, b Batch, branch string) (string, error) {
	out, err := c.git(ctx, dir, b.Created, "ls-remote", "--", b.Remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || !commitID.MatchString(fields[0]) || fields[1] != "refs/heads/"+branch {
		return "", errors.New("Git returned an unexpected remote reference")
	}
	return fields[0], nil
}

// Build merges frozen PR heads in order, always using merge commits. A conflict
// excludes only that PR. Other errors stop the trial without publishing main.
func (c Client) Build(ctx context.Context, b Batch, save func(Result) error) (result Result, err error) {
	err = c.repository(ctx, b, func(dir string) error {
		if len(b.Heads) == 0 || b.Created.IsZero() {
			return errors.New("a trial needs PR heads and a fixed creation time")
		}
		if err := c.fetch(ctx, dir, b); err != nil {
			return err
		}
		if _, err := c.git(ctx, dir, b.Created, "checkout", "--quiet", "--detach", b.Base); err != nil {
			return err
		}
		for _, head := range b.Heads {
			args := []string{"-c", "core.hooksPath=" + os.DevNull, "merge", "--no-ff", "--no-edit", "--no-verify", "--no-gpg-sign"}
			if message := b.Messages[head]; message != "" {
				args = append(args, "-m", message)
			}
			if _, mergeErr := c.git(ctx, dir, b.Created, append(args, head)...); mergeErr != nil {
				unmerged, readErr := c.git(ctx, dir, b.Created, "ls-files", "--unmerged")
				if readErr != nil || unmerged == "" {
					return errors.Join(mergeErr, readErr)
				}
				if _, err := c.git(ctx, dir, b.Created, "merge", "--abort"); err != nil {
					return errors.Join(mergeErr, err)
				}
				result.Conflicts = append(result.Conflicts, head)
				continue
			}
			result.Included = append(result.Included, head)
		}
		head, err := c.git(ctx, dir, b.Created, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		result.Head = head
		if len(result.Included) == 0 {
			return nil
		}
		// Save the exact commit before the remote side effect. A restarted
		// caller can clean up a pushed branch even if the response was lost.
		if save != nil {
			if err := save(result); err != nil {
				return err
			}
		}
		existing, err := c.remoteHead(ctx, dir, b, b.Branch)
		if err != nil {
			return err
		}
		if existing == head {
			return nil
		}
		if existing != "" {
			return errors.New("merge trial branch already belongs to a different commit")
		}
		_, err = c.git(ctx, dir, b.Created, "push", "--quiet", "--force-with-lease=refs/heads/"+b.Branch+":", "--", b.Remote, head+":refs/heads/"+b.Branch)
		return err
	})
	return result, err
}

// Publish fast-forwards the exact expected main commit to the tested commit.
// The explicit lease rejects outside movement, even when it would allow a
// normal fast-forward. The ancestry check makes this an actual fast-forward.
func (c Client) Publish(ctx context.Context, b Batch, head string) error {
	return c.publish(ctx, b, head, true)
}

// PublishHead publishes a tested head with the same ancestry and atomic base
// lease as Publish, but does not require a merge-trial branch to exist.
func (c Client) PublishHead(ctx context.Context, b Batch, head string) error {
	return c.publish(ctx, b, head, false)
}

func (c Client) publish(ctx context.Context, b Batch, head string, requireTrialBranch bool) error {
	if !commitID.MatchString(head) {
		return errors.New("invalid tested commit")
	}
	return c.repositoryWithBranch(ctx, b, requireTrialBranch, func(dir string) error {
		main, err := c.remoteHead(ctx, dir, b, b.DefaultBranch)
		if err != nil {
			return err
		}
		if main == head {
			return nil
		}
		if main != b.Base {
			return ErrMainMoved
		}
		if requireTrialBranch {
			tested, err := c.remoteHead(ctx, dir, b, b.Branch)
			if err != nil {
				return err
			}
			if tested != head {
				return errors.New("tested trial branch moved or disappeared")
			}
		}
		if _, err := c.git(ctx, dir, b.Created, "fetch", "--quiet", "--no-tags", "--", b.Remote, b.Base, head); err != nil {
			return err
		}
		if _, err := c.git(ctx, dir, b.Created, "merge-base", "--is-ancestor", b.Base, head); err != nil {
			return errors.New("tested commit does not fast-forward the expected main")
		}
		_, err = c.git(ctx, dir, b.Created, "push", "--quiet", "--force-with-lease=refs/heads/"+b.DefaultBranch+":"+b.Base, "--", b.Remote, head+":refs/heads/"+b.DefaultBranch)
		if err != nil {
			current, readErr := c.remoteHead(ctx, dir, b, b.DefaultBranch)
			if readErr == nil && current == head {
				return nil
			}
			if readErr == nil && current != b.Base {
				return ErrMainMoved
			}
		}
		return err
	})
}

// Delete is idempotent and refuses to remove a trial branch changed by another actor.
func (c Client) Delete(ctx context.Context, b Batch, head string) error {
	if !commitID.MatchString(head) {
		return errors.New("invalid tested commit")
	}
	return c.repository(ctx, b, func(dir string) error {
		current, err := c.remoteHead(ctx, dir, b, b.Branch)
		if err != nil || current == "" {
			return err
		}
		if current != head {
			return errors.New("refusing to delete a changed trial branch")
		}
		_, err = c.git(ctx, dir, b.Created, "push", "--quiet", "--force-with-lease=refs/heads/"+b.Branch+":"+head, "--", b.Remote, ":refs/heads/"+b.Branch)
		return err
	})
}

func (c Client) fetch(ctx context.Context, dir string, b Batch) error {
	args := []string{"fetch", "--quiet", "--no-tags", "--", b.Remote, b.Base}
	for _, head := range b.Heads {
		if !commitID.MatchString(head) {
			return errors.New("invalid PR commit")
		}
		args = append(args, head)
	}
	_, err := c.git(ctx, dir, b.Created, args...)
	return err
}

// PrepareReview exports only base source and frozen PR patches. It neither
// merges the patches nor exposes a repository with later refs to the reader.
func (c Client) PrepareReview(ctx context.Context, b Batch, target string) error {
	return c.repository(ctx, b, func(dir string) error {
		if err := c.fetch(ctx, dir, b); err != nil {
			return err
		}
		base := filepath.Join(target, "base")
		if err := os.MkdirAll(base, 0700); err != nil {
			return err
		}
		if _, err := c.git(ctx, dir, b.Created, "--work-tree="+base, "checkout", "--force", b.Base, "--", "."); err != nil {
			return err
		}
		if err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return os.Remove(path)
			}
			if !entry.IsDir() {
				return os.Chmod(path, 0444)
			}
			return nil
		}); err != nil {
			return err
		}
		for i, head := range b.Heads {
			patch, err := c.git(ctx, dir, b.Created, "diff", "--no-ext-diff", "--no-textconv", "--binary", b.Base+"..."+head, "--")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(target, fmt.Sprintf("pr-%d.patch", i+1)), []byte(patch+"\n"), 0444); err != nil {
				return err
			}
		}
		return nil
	})
}
