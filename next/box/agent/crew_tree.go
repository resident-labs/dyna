package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const (
	// pushAttempts is how often a finished step tries to reach the remote. Failing means redoing all of its work.
	pushAttempts = 3
	// saveTimeout bounds one attempt. A worker's own timeout has already ended by then.
	saveTimeout = 30 * time.Minute
)

// pushDelay is the wait between attempts. It is a variable so that tests can shorten it.
var pushDelay = 10 * time.Second

// treeResult is what a worktree step stores. The work itself is on the remote, at Branch and Commit.
type treeResult struct {
	Step   string   `json:"step"`
	Text   string   `json:"text,omitempty"`
	Output any      `json:"output,omitempty"`
	Branch string   `json:"branch"`
	Commit string   `json:"commit"`
	Files  []string `json:"files"`
}

// treeDir is a step's folder, beside the main tree so that git status there never sees it.
func (c *Crew) treeDir(name string) string { return c.config.Workdir + ".worktrees/" + name }

// addTree gives a step a new tree, and clears what a lost attempt at it left.
func (c *Crew) addTree(ctx context.Context, dir string, args ...string) error {
	c.repo.Lock()
	defer c.repo.Unlock()
	c.dropTree(ctx, dir)
	_, err := runGit(ctx, c.config.Workdir, "", append([]string{"worktree", "add"}, args...)...)
	return err
}

func (c *Crew) dropTree(ctx context.Context, dir string) {
	_, _ = runGit(ctx, c.config.Workdir, "", "worktree", "remove", "--force", dir)
	_ = os.RemoveAll(dir)
	_, _ = runGit(ctx, c.config.Workdir, "", "worktree", "prune")
}

func (c *Crew) removeTree(ctx context.Context, dir string) {
	c.repo.Lock()
	defer c.repo.Unlock()
	c.dropTree(context.WithoutCancel(ctx), dir)
}

// treeWorker runs a worker in its own tree and saves what it leaves before the step can be stored, so a stored step always has
// its work on the remote. The tree goes once its work is saved, so a script finds the same thing here as in a replacement box.
func (c *Crew) treeWorker(ctx context.Context, key, name string, profile contract.WorkflowProfile, prompt string, opts agentOpts) stepOutcome {
	if c.config.Base == "" {
		return stepOutcome{reason: "worktree: true needs a repository, and this run has none"}
	}
	dir, branch := c.treeDir(key[:12]), c.config.Branch+"-"+key[:12]
	if err := c.addTree(ctx, dir, "-B", branch, dir, c.config.Base); err != nil {
		return stepOutcome{reason: err.Error()}
	}
	defer c.removeTree(ctx, dir)
	out := c.worker(ctx, key, profile, prompt, opts, dir)
	if out.reason != "" || ctx.Err() != nil {
		return out
	}
	result := treeResult{Step: name, Branch: branch}
	if len(opts.Schema) > 0 {
		result.Output = out.value
	} else {
		result.Text, _ = out.value.(string)
	}
	var err error
	if result.Commit, result.Files, err = c.saveTree(ctx, dir, name, branch); err != nil {
		out.value, out.reason = nil, fmt.Sprintf("the worker's work did not reach %s: %s", branch, err)
		return out
	}
	out.value = result
	return out
}

// saveTree commits what a worker left and pushes its branch. A rerun replaces what a lost attempt pushed there.
func (c *Crew) saveTree(ctx context.Context, dir, name, branch string) (string, []string, error) {
	git := func(args ...string) (string, error) { return runGit(ctx, dir, "", args...) }
	if _, err := git("add", "-A"); err != nil {
		return "", nil, err
	}
	status, err := git("status", "--porcelain")
	if err != nil {
		return "", nil, err
	}
	if status != "" {
		if _, err := git("commit", "--no-verify", "-m", name); err != nil {
			return "", nil, err
		}
	}
	commit, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", nil, err
	}
	changed, err := git("diff", "--name-only", "--no-renames", "-z", c.config.Base, commit)
	if err != nil {
		return "", nil, err
	}
	if err := c.push(ctx, dir, commit, branch); err != nil {
		return "", nil, err
	}
	files := strings.FieldsFunc(changed, func(r rune) bool { return r == 0 })
	if files == nil {
		files = []string{}
	}
	return commit, files, nil
}

func (c *Crew) push(ctx context.Context, dir, commit, branch string) error {
	var err error
	for attempt := 1; attempt <= pushAttempts; attempt++ {
		bound, cancel := context.WithTimeout(ctx, saveTimeout)
		_, err = runGit(bound, dir, "", "push", "--force", "origin", commit+":refs/heads/"+branch)
		cancel()
		if err == nil || attempt == pushAttempts {
			break
		}
		select {
		case <-time.After(pushDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// restore brings a stored step's branch into this box, which may never have seen it.
func (c *Crew) restore(ctx context.Context, call crewCall, reply crewReply) crewReply {
	if !reply.stored {
		return reply
	}
	var saved struct {
		Branch string `json:"branch"`
	}
	data, _ := json.Marshal(reply.Value)
	if err := json.Unmarshal(data, &saved); err != nil || saved.Branch == "" {
		return reply
	}
	c.repo.Lock()
	defer c.repo.Unlock()
	if _, err := runGit(ctx, c.config.Workdir, "", "fetch", "--update-head-ok", "origin", "+refs/heads/"+saved.Branch+":refs/heads/"+saved.Branch); err != nil {
		reason := fmt.Sprintf("stored work on %s did not come back: %s", saved.Branch, err)
		payload, _ := json.Marshal(reason)
		return c.step(ctx, crewCall{Op: "failed", Parent: call.Parent, Phase: call.Phase, Payload: payload}, stepInfo{kind: contract.WorkflowStepLog, detail: reason}, func(context.Context, string) stepOutcome {
			return stepOutcome{reason: reason}
		})
	}
	return reply
}

type mergeOpts struct {
	Base  string `json:"base"`
	Label string `json:"label"`
	Phase string `json:"phase"`
}

// mergeEntry is one item of merge's list: a worktree step's result, or the entry parallel leaves for a step that failed.
type mergeEntry struct {
	treeResult
	Failed bool   `json:"failed"`
	Reason string `json:"reason"`
}

// mergeCall merges worktree steps into the run's branch. A conflict is a result, not a failure of the step, but each one joins
// the journal as a failed entry so that the run's end counts it. Failed entries are not kept across a resume, so a stored merge
// writes them again here.
func (c *Crew) mergeCall(ctx context.Context, call crewCall) crewReply {
	var opts mergeOpts
	refused := strictOpts(call.Opts, &opts)
	var entries []mergeEntry
	notResults := errors.New("it takes the results of agent() with worktree: true")
	if err := json.Unmarshal(call.Payload, &entries); refused == nil && err != nil {
		refused = notResults
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Step)
		if refused == nil && !entry.Failed && (entry.Branch == "" || entry.Commit == "") {
			refused = notResults
		}
	}
	info := stepInfo{kind: contract.WorkflowStepCommand, label: opts.Label, phase: opts.Phase, detail: "merge " + strings.Join(names, ", ")}
	reply := c.restore(ctx, call, c.step(ctx, call, info, func(ctx context.Context, key string) stepOutcome {
		if refused != nil {
			return stepOutcome{reason: "merge: " + refused.Error()}
		}
		return c.merge(ctx, key, entries, opts)
	}))
	if reply.Error != "" {
		return reply
	}
	var result contract.WorkflowMerge
	data, _ := json.Marshal(reply.Value)
	_ = json.Unmarshal(data, &result)
	for _, conflict := range result.Conflicts {
		if conflict.Reason != "" {
			continue // Its failed step is in the journal already.
		}
		payload, _ := json.Marshal(conflict)
		entry := crewCall{Op: "conflict", Parent: call.Parent, Phase: call.Phase, Payload: payload}
		c.step(ctx, entry, stepInfo{kind: contract.WorkflowStepLog, label: "merge conflict: " + conflict.Step}, func(context.Context, string) stepOutcome {
			return stepOutcome{reason: "conflicts in " + strings.Join(conflict.Files, ", ")}
		})
	}
	return reply
}

// merge runs in a tree of its own, because workers may be writing in the main one. The run's branch only ever gets clean
// merges: a step that conflicts is aborted and the next goes on from where the last clean merge left it.
func (c *Crew) merge(ctx context.Context, key string, entries []mergeEntry, opts mergeOpts) stepOutcome {
	base := opts.Base
	if base == "" {
		base = c.config.Base
	}
	if base == "" {
		return stepOutcome{reason: "merge needs a repository, and this run has none"}
	}
	dir := c.treeDir("merge-" + key[:12])
	if err := c.addTree(ctx, dir, "--detach", dir, base); err != nil {
		return stepOutcome{reason: err.Error()}
	}
	defer c.removeTree(ctx, dir)
	result := contract.WorkflowMerge{Branch: c.config.Branch + "-run", Merged: []string{}, Conflicts: []contract.WorkflowConflict{}}
	for _, entry := range entries {
		if entry.Failed {
			result.Conflicts = append(result.Conflicts, contract.WorkflowConflict{Step: entry.Step, Files: []string{}, Reason: entry.Reason})
			continue
		}
		_, err := runGit(ctx, dir, "", "merge", "--no-ff", "--no-verify", "-m", "merge "+entry.Step, entry.Commit)
		if err == nil {
			result.Merged = append(result.Merged, entry.Step)
			continue
		}
		unmerged, listed := runGit(ctx, dir, "", "diff", "--name-only", "--diff-filter=U", "-z")
		if listed != nil || unmerged == "" {
			return stepOutcome{reason: err.Error()}
		}
		if _, err := runGit(ctx, dir, "", "merge", "--abort"); err != nil {
			return stepOutcome{reason: err.Error()}
		}
		files := strings.FieldsFunc(unmerged, func(r rune) bool { return r == 0 })
		result.Conflicts = append(result.Conflicts, contract.WorkflowConflict{Step: entry.Step, Branch: entry.Branch, Files: files})
	}
	commit, err := runGit(ctx, dir, "", "rev-parse", "HEAD")
	if err != nil {
		return stepOutcome{reason: err.Error()}
	}
	result.Commit = commit
	if err := c.push(ctx, dir, commit, result.Branch); err != nil {
		return stepOutcome{reason: fmt.Sprintf("the merge did not reach %s: %s", result.Branch, err)}
	}
	return stepOutcome{value: result}
}
