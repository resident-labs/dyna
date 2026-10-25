package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dyna-agent/internal/localci"
)

// localCIConfigState describes repository configuration to PR views. It is
// typed so a client can word the state without displaying a Git or YAML error.
type localCIConfigState string

const (
	localCIConfigNotConfigured localCIConfigState = "not-configured"
	localCIConfigHeadMissing   localCIConfigState = "head-missing"
)

// localCIPlan separates the two revisions involved in a local run. Execution
// comes from the head so a PR can repair commands. The required graph and file
// selection come from the base so the same PR cannot remove its own gate.
type localCIPlan struct {
	execution   *localci.Definition
	required    *localci.Definition
	suites      []localci.Suite
	baseMissing bool
	headMissing bool
}

func (p localCIPlan) runnable() bool {
	return p.execution != nil && p.required != nil
}

func (s *Service) registeredJobForRecord(r Record) (registeredCIJob, error) {
	job := registeredCIJob{record: r, head: r.Head, branch: r.Branch, base: r.Base, trial: strings.HasPrefix(r.Branch, "dyna/merge-trial/")}
	if !job.trial {
		return job, nil
	}
	if s.Store == nil {
		return job, fmt.Errorf("local CI merge trial has no stored base SHA")
	}
	st, err := s.Store.Read()
	if err != nil {
		return job, err
	}
	for _, batch := range st.MergeTrials {
		if batch.Branch == r.Branch && batch.Head == r.Head {
			job.base = batch.Base
			return job, nil
		}
		for _, stack := range batch.Stacks {
			if stack.Branch == r.Branch && stack.Head == r.Head {
				job.base = batch.Base
				return job, nil
			}
		}
	}
	if headSHAPattern.MatchString(job.base) {
		return job, nil
	}
	return job, fmt.Errorf("local CI merge trial has no verified base SHA")
}

func (s *Service) planRegisteredCI(ctx context.Context, job registeredCIJob) (localCIPlan, error) {
	var plan localCIPlan
	base := job.base
	if base == "" {
		base = job.record.Base
	}
	if !headSHAPattern.MatchString(base) {
		return plan, fmt.Errorf("local CI requires a verified base SHA")
	}
	required, err := localci.LoadDefinition(ctx, job.record.RepoPath, base)
	if errors.Is(err, localci.ErrDefinitionNotFound) {
		plan.baseMissing = true
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.required = required
	execution, err := localci.LoadDefinition(ctx, job.record.RepoPath, job.head)
	if errors.Is(err, localci.ErrDefinitionNotFound) {
		plan.headMissing = true
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.execution = execution
	if job.trial {
		plan.suites = required.WorkflowSuites()
		return plan, nil
	}
	files, err := gitRead(ctx, job.record.RepoPath, "diff", "--name-only", base+"..."+job.head)
	if err != nil {
		return plan, err
	}
	plan.suites = required.SuitesForFiles(strings.Split(files, "\n"))
	return plan, nil
}
