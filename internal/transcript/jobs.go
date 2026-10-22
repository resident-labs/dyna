package transcript

import (
	"slices"

	"dyna-agent/internal/shells"
)

// LinkJobs gives the shell calls in items that name no job the job they
// started, so every call has one terminal block. jobs are the session's
// jobs, as the store lists them.
//
// A shell result that names its job is exact and always wins: its jobs are
// set aside first. Output and stop calls name a job too, but they refer to an
// existing one and never claim a launch, so they set nothing aside. Only a call with no such result is guessed: a call still
// running, or one that ended before results led with the job ID. A guess pairs
// calls and jobs in start order when the command and description match. It
// lasts only until the call's result arrives, because the next build reads the
// ID from that result.
//
// TODO: legacy/shim remove later — the guess keeps calls from before results named their job linked; remove once no kept transcript predates the "Job <id>." lead, leaving only the still-running case.
func LinkJobs(items []Item, jobs []shells.Info) {
	claimed := map[string]bool{}
	for _, it := range items {
		if it.Kind == ToolUse && it.Tool.JobOp == "run" && it.Tool.JobID != "" {
			claimed[it.Tool.JobID] = true
		}
	}
	free := slices.DeleteFunc(slices.Clone(jobs), func(in shells.Info) bool { return claimed[in.ID] || len(in.Cmd) == 0 })
	slices.SortStableFunc(free, func(a, b shells.Info) int { return a.Started.Compare(b.Started) })
	for i := range items {
		t := &items[i].Tool
		if items[i].Kind != ToolUse || t.JobOp != "run" || t.JobID != "" {
			continue
		}
		at := slices.IndexFunc(free, func(in shells.Info) bool {
			return in.Name == t.Description && UnwrapShell(in.Cmd[len(in.Cmd)-1]) == t.Detail
		})
		if at >= 0 {
			t.JobID = free[at].ID
			free = slices.Delete(free, at, at+1)
		}
	}
}
