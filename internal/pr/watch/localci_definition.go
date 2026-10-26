package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dyna-agent/internal/localci"
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
	// bootstrap marks a head that adds local CI where the base has none. The head
	// definition is then both required and executed: the base required nothing,
	// and the trial runs the approved PR code anyway.
	bootstrap bool
}

func (p localCIPlan) runnable() bool {
	return p.execution != nil && p.required != nil
}

func (s *Service) registeredJobForRecord(r Record) (registeredCIJob, error) {
	trial := strings.HasPrefix(r.Branch, "dyna/merge-trial/")
	job := registeredCIJob{record: r, head: r.Head, branch: r.Branch, base: r.Base, trial: trial, wholeSuite: trial}
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
		head, headErr := localci.LoadDefinition(ctx, job.record.RepoPath, job.head)
		if errors.Is(headErr, localci.ErrDefinitionNotFound) {
			plan.baseMissing = true
			return plan, nil
		}
		if headErr != nil {
			return plan, headErr
		}
		plan.required, plan.execution, plan.bootstrap = head, head, true
		required = head
		err = nil
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
	if job.wholeSuite {
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
