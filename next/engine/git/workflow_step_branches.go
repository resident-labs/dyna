package git

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
)

// WorkflowStepBranchRetention starts when the last run referencing a step branch ends.
const WorkflowStepBranchRetention = 14 * 24 * time.Hour

// DeleteWorkflowStepBranches deletes expired refs still at their journaled tip. Grouped reads avoid one request per
// historical branch on every sweep. The empty local repository needs no mirror or objects to push a deletion.
func DeleteWorkflowStepBranches(ctx context.Context, runner Runner, roomToken func(context.Context, contract.RoomID) (string, string, error), accounts *prs.Accounts, branches []store.WorkflowStepBranch) error {
	if len(branches) == 0 {
		return nil
	}
	dir, err := os.MkdirTemp("", "dyna-step-expiry-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err := runner.Run(ctx, nil, "init", "--bare", dir); err != nil {
		return err
	}
	// The store orders these by room and repository.
	for len(branches) != 0 && ctx.Err() == nil {
		first := branches[0]
		n := 1
		for n < len(branches) && branches[n].Room == first.Room && strings.EqualFold(string(branches[n].Repo), string(first.Repo)) {
			n++
		}
		bound, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := deleteWorkflowStepBranches(bound, runner, roomToken, accounts, dir, branches[:n])
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("workflow step branch expiry", "room", first.Room, "repo", first.Repo, "error", err)
		}
		branches = branches[n:]
	}
	return ctx.Err()
}

func deleteWorkflowStepBranches(ctx context.Context, runner Runner, roomToken func(context.Context, contract.RoomID) (string, string, error), accounts *prs.Accounts, dir string, branches []store.WorkflowStepBranch) error {
	first := branches[0]
	token, login, err := roomToken(ctx, first.Room)
	if err != nil {
		return err
	}
	if token == "" {
		return MissingTokenError{}
	}
	env := gitHubAuthEnv(token)
	remote := "https://github.com/" + string(first.Repo) + ".git"
	out, err := runner.Run(ctx, env, "ls-remote", "--refs", "--heads", "--", remote)
	if err != nil {
		return fmt.Errorf("list remote heads: %w", err)
	}
	tips := make(map[string]string)
	for line := range strings.SplitSeq(string(out), "\n") {
		if oid, ref, ok := strings.Cut(line, "\t"); ok {
			tips[ref] = oid
		}
	}
	var refs []string
	for _, step := range branches {
		ref := "refs/heads/" + step.Branch
		if validOID(step.Commit) && tips[ref] == step.Commit {
			refs = append(refs, ref)
		}
	}
	if login != "" {
		login += " of room " + string(first.Room)
	}
	open, err := accounts.Client(token, login).OpenPRHeads(ctx, first.Repo, refs)
	if err != nil {
		return fmt.Errorf("check open PR heads: %w", err)
	}
	var failures []error
	for batch := range slices.Chunk(branches, 100) {
		if ctx.Err() != nil {
			break
		}
		args := []string{"--git-dir", dir, "-c", "core.hooksPath=" + os.DevNull, "push", "--porcelain"}
		var deletes []string
		for _, step := range batch {
			ref := "refs/heads/" + step.Branch
			if protected, checked := open[ref]; checked && !protected {
				args = append(args, "--force-with-lease="+ref+":"+step.Commit)
				deletes = append(deletes, ":"+ref)
			}
		}
		if len(deletes) == 0 {
			continue
		}
		args = append(args, "--", remote)
		if _, err := runner.Run(ctx, env, append(args, deletes...)...); err != nil {
			failures = append(failures, fmt.Errorf("delete batch of %d expired heads: %w", len(deletes), err))
		}
	}
	return errors.Join(append(failures, ctx.Err())...)
}
