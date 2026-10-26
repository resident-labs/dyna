// Warm runs the provider's cache-warming job on a node, so the job checks of
// later runs restore a build cache instead of compiling from nothing.
package localci

import (
	"context"
	"fmt"
	"time"
)

// warmSuite is the suite a warm run executes. It is not part of any repository
// definition: the run builds its own one-check definition below.
const warmSuite Suite = "cache-warm"

// WarmRequest asks one node to warm the act cache that the job checks of this
// repository restore from. The workflow scopes its cache-warming job to the
// default branch, so a warm run only makes sense for a commit on that branch.
type WarmRequest struct {
	// Repo is the repository URL and RepoDir its checkout on the engine.
	Repo, RepoDir string
	// RoomID is the room whose machine takes the run, which its host-check
	// grant is read for.
	RoomID string
	// SHA is the default branch commit to warm from.
	SHA string
	// Workflow is the provider workflow file and Job the job inside it that
	// fills the cache. A repository without such a job cannot be warmed.
	Workflow, Job string
	// Branch is the default branch the workflow warms the cache for.
	Branch string
	// Timeout bounds the whole warm run.
	Timeout time.Duration
}

// WarmDefinition is the one-check definition a warm run executes. It names the
// provider's cache-warming job at the same workflow path a repository check
// uses, so act writes into the same cache store the checks read.
func WarmDefinition(workflow, job string) ([]byte, error) {
	if workflow == "" || job == "" {
		return nil, fmt.Errorf("a warm run needs a workflow and a job")
	}
	body := fmt.Sprintf(`version: 1
trial: [%s]
checks:
  - id: %s
    job:
      workflow: %s
      name: %s
    reports: none
suites:
  - id: %s
    checks: [%s]
`, warmSuite, warmSuite, workflow, job, warmSuite, warmSuite)
	if _, err := ParseDefinition([]byte(body)); err != nil {
		return nil, fmt.Errorf("warm %s in %s: %w", job, workflow, err)
	}
	return []byte(body), nil
}

// Warm runs the cache-warming job on this runner's node. It takes a slot like
// every other check, so a warm run never starves a pull request's checks, and
// it writes into the same act cache store the job checks read.
func (r Runner) Warm(ctx context.Context, req WarmRequest) (Result, error) {
	body, err := WarmDefinition(req.Workflow, req.Job)
	if err != nil {
		return Result{}, err
	}
	definition, err := ParseDefinition(body)
	if err != nil {
		return Result{}, err
	}
	return r.Run(ctx, warmRunRequest(definition, req))
}

// warmRunRequest is the run one warm request asks for. The checkout path is the
// repository, because the runner reads it as a local directory, and the result
// is not saved: a warm run proves nothing about the commit, and its row would
// replace the test evidence real checks saved for the same commit.
func warmRunRequest(definition *Definition, req WarmRequest) Request {
	return Request{
		Definition: definition,
		Repo:       req.RepoDir,
		RepoDir:    req.RepoDir,
		RoomID:     req.RoomID,
		SHA:        req.SHA,
		Suites:     []Suite{warmSuite},
		Timeout:    req.Timeout,
		DeferSave:  true,
		// The workflow scopes its cache-warming job to the default branch, so
		// the run sends that branch's push event.
		Event:  "push",
		Branch: req.Branch,
	}
}
