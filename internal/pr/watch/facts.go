package watch

import "dyna-agent/internal/pr/facts"

// GitHubFacts combines a fresh snapshot with facts from the single batched head query.
// A changed head cannot inherit approval or check results from an earlier head.
func (s Snapshot) GitHubFacts(r Record, requiredChecks ...string) facts.GitHub {
	g := facts.FromRecord(r)
	g.Head = s.Pull.Head.SHA
	g.Base = s.Pull.Base.Ref
	g.CIHead = s.Pull.Head.SHA
	g.CI, _, _, _ = s.CIStatus(requiredChecks...)
	if g.Head != r.Head || s.Pull.State != "open" {
		g.Mergeable = nil
	}
	return g
}
