package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

// requestChars keeps a question's line short enough for several in the section's budget.
const requestChars = 80

// holdsChars is the budget of dyna_holds, which holdsLine also keeps to, so its first line always fits.
const holdsChars = 1000

// roomChars is the budget of dyna_room.
const roomChars = 400

// workflowsChars is the budget of dyna_workflows.
const workflowsChars = 300

// rootApprovalNames name the root-approves kinds as Settings > Approvals does.
var rootApprovalNames = map[contract.RootApproval]string{
	contract.RootApprovesHostexec: "host exec",
	contract.RootApprovesAddons:   "global add-ons",
	contract.RootApprovesHosts:    "room host rules",
	contract.RootApprovesPools:    "node pools",
}

func renderContextSection(key contract.ContextSectionKey, lines []string, maxChars int, full bool, rowLimit int) contract.ContextSection {
	if full {
		return contract.ContextSection{Key: key, Text: strings.Join(lines, "\n")}
	}
	var kept []string
	chars := 0
	for i, line := range lines {
		if rowLimit > 0 && i >= rowLimit {
			break
		}
		n := utf8.RuneCountInString(line)
		remaining := len(lines) - i - 1
		suffix := ""
		if remaining > 0 {
			suffix = fmt.Sprintf("\n%d more: context show", remaining)
		}
		sep := 0
		if len(kept) > 0 {
			sep = 1
		}
		if chars+sep+n+utf8.RuneCountInString(suffix) > maxChars {
			break
		}
		kept = append(kept, line)
		chars += sep + n
	}
	if more := len(lines) - len(kept); more > 0 {
		kept = append(kept, fmt.Sprintf("%d more: context show", more))
	}
	return contract.ContextSection{Key: key, Text: strings.Join(kept, "\n")}
}

// prLine says a PR's state in a few words. It holds nothing from GitHub's free text, so the snapshot needs no wrapper.
func prLine(p store.ContextPR) string {
	var parts []string
	if p.Draft {
		parts = append(parts, "draft")
	}
	switch p.CI {
	case contract.PrCISuccess:
		parts = append(parts, "CI passed")
	case contract.PrCIFailure:
		parts = append(parts, "CI failed")
	case contract.PrCIPending:
		parts = append(parts, "CI running")
	case contract.PrCINone:
		parts = append(parts, "no CI")
	default:
		parts = append(parts, "CI not read yet")
	}
	switch p.Review {
	case contract.PrApproved:
		parts = append(parts, "approved")
	case contract.PrChangesRequested:
		parts = append(parts, "changes requested")
	default:
		parts = append(parts, "not approved")
	}
	switch p.Merge {
	case contract.PrMergeClean:
		parts = append(parts, "mergeable")
	case contract.PrMergeConflict:
		parts = append(parts, "conflict")
	case contract.PrMergeBehind:
		parts = append(parts, "behind base")
	case contract.PrMergeBlocked:
		parts = append(parts, "merge blocked")
	}
	return strings.Join(parts, ", ")
}

// holdsLine states what the session still holds. The turn being dispatched is not one of those: it is open because it is
// starting. A budget of 0 names everything.
func holdsLine(held store.Held, budget int) string {
	var names []string
	used := 0
	for _, item := range held {
		if item.Kind == contract.ResponsibilityTurn {
			continue
		}
		name := room.ResponsibilityName(item)
		if used += len(name) + 2; budget > 0 && used > budget {
			break
		}
		names = append(names, name)
	}
	more := len(held) - held.Count(contract.ResponsibilityTurn) - len(names)
	if more > 0 {
		names = append(names, fmt.Sprintf("%d more: context show", more))
	}
	if len(names) == 0 {
		return ""
	}
	return "You still hold: " + strings.Join(names, ", ") + "."
}

// contextLine folds session-written text to one line of at most n characters.
func contextLine(text string, n int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return string(runes)
}

func (s *Service) ContextSnapshot(ctx context.Context, caller contract.Principal, req contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ContextSnapshotResponse{}, forbidden()
	}
	switch req.Section {
	case "", contract.ContextGoal, contract.ContextChildren, contract.ContextRequests, contract.ContextHolds, contract.ContextProfiles, contract.ContextUsage, contract.ContextRoom, contract.ContextWorkflows:
	default:
		return contract.ContextSnapshotResponse{}, invalid("unknown context section")
	}
	data, err := s.store.ContextDataForSession(ctx, caller.Session)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	result := contract.ContextSnapshotResponse{Sections: []contract.ContextSection{}}
	add := func(key contract.ContextSectionKey, lines []string, maxChars, limit int) {
		if len(lines) > 0 && (req.Section == "" || req.Section == key) {
			result.Sections = append(result.Sections, renderContextSection(key, lines, maxChars, req.Section != "", limit))
		}
	}
	var lines []string
	for _, g := range data.Goals {
		lines = append(lines, fmt.Sprintf("%s: %s", g.ID, contextLine(g.Text, 300)))
		if g.DoneWhen != "" {
			lines = append(lines, "Done when: "+contextLine(g.DoneWhen, 200))
		}
		for _, item := range g.Items {
			lines = append(lines, "- "+contextLine(item, 120))
		}
	}
	add(contract.ContextGoal, lines, 800, 0)
	lines = nil
	for _, c := range data.Children {
		line := fmt.Sprintf("%s: %s, %s", c.Name, c.Profile, c.State)
		if s.startStatus != nil && (c.State == contract.BoxCreating || c.State == contract.BoxStarting) {
			if start := s.startStatus(c.Box); start != nil {
				line += ", " + start.Label(s.now())
			}
		}
		if c.Item != "" {
			line += ", " + contextLine(c.Item, 80)
		}
		if c.Waiting != nil {
			line += ", waiting: " + c.Waiting.Label()
		} else if c.Asking {
			line += ", waiting for the owner's answer"
		}
		lines = append(lines, line)
	}
	add(contract.ContextChildren, lines, 1000, 8)
	lines = nil
	for _, r := range data.Requests {
		line := fmt.Sprintf("%s: %s, waiting", r.ID, r.Kind)
		if r.Text != "" {
			line += ": " + excerpt(r.Text, requestChars)
		}
		lines = append(lines, line)
	}
	add(contract.ContextRequests, lines, 600, 0)
	lines = nil
	budget := holdsChars / 2
	if req.Section != "" {
		budget = 0
	}
	if line := holdsLine(data.Held, budget); line != "" {
		lines = append(lines, line)
	}
	for _, p := range data.PRs {
		lines = append(lines, fmt.Sprintf("PR %s#%d: %s", p.Repo, p.Number, prLine(p)))
	}
	add(contract.ContextHolds, lines, holdsChars, 0)
	if s.maxDepth == 0 || data.Depth < s.maxDepth {
		lines = nil
		for _, p := range s.profiles.List() {
			line := fmt.Sprintf("%s: taste %d, diagnostics %d, intelligence %d, cost %d", p.Name, p.Taste, p.Diagnostics, p.Intelligence, p.Cost)
			if p.MaxConcurrent > 0 {
				line += fmt.Sprintf("; max concurrent %d", p.MaxConcurrent)
			}
			lines = append(lines, line)
		}
		add(contract.ContextProfiles, lines, 1200, 0)
	}
	if s.usage != nil {
		add(contract.ContextUsage, usageLines(data.Accounts, s.usage.Latest(), s.now()), usageChars, 0)
	}
	// One line per room setting that is not at its default and changes what root may do; a new setting adds its line here.
	lines = nil
	var decide []string
	for _, kind := range contract.AllRootApprovals {
		if slices.Contains(data.RootApproves, kind) {
			decide = append(decide, rootApprovalNames[kind])
		}
	}
	if len(decide) > 0 {
		lines = append(lines, "Approvals you decide: "+strings.Join(decide, ", "))
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	// What the room allows every session, when it is on: the GitHub writes of its githubWrites setting (spec 7.9).
	granted, err := s.GitHubWritesOf(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	if granted.On() {
		groups := make([]string, len(granted.Groups))
		for i, group := range granted.Groups {
			groups[i] = string(group)
		}
		repos := make([]string, len(granted.Repos))
		for i, repo := range granted.Repos {
			repos[i] = string(repo)
		}
		lines = append(lines, "GitHub writes: "+strings.Join(groups, ", ")+" on "+strings.Join(repos, ", "))
	}
	add(contract.ContextRoom, lines, roomChars, 0)
	// What the room's workflow settings change from the defaults, for every session; limits and defaults are named by the refusal that
	// meets them.
	effective, err := s.store.WorkflowEffective(ctx, session.Room)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	lines = nil
	switch effective.Start {
	case contract.WorkflowStartLeads:
		lines = append(lines, "Workflows: started by leads (root and sessions that have started a child)")
	case contract.WorkflowStartRoot:
		lines = append(lines, "Workflows: started by root")
	case contract.WorkflowStartOff:
		lines = append(lines, "Workflows: no session starts one; the owner does")
	}
	var off []string
	for _, feature := range contract.AllWorkflowFeatures {
		if !effective.Allows(feature) {
			name := string(feature)
			if feature != contract.WorkflowFeatureResume {
				name += "()"
			}
			off = append(off, name)
		}
	}
	if len(off) > 0 {
		lines = append(lines, "Off: "+strings.Join(off, ", "))
	}
	add(contract.ContextWorkflows, lines, workflowsChars, 0)
	return result, nil
}

// excerpt fits text on one row of a section.
func excerpt(text string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}
