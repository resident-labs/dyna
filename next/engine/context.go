package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// requestChars keeps a question's line short enough for several in the section's budget.
const requestChars = 80

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
	case "", contract.ContextGoal, contract.ContextChildren, contract.ContextRequests, contract.ContextPRs, contract.ContextProfiles, contract.ContextUsage:
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
		if c.Item != "" {
			line += ", " + contextLine(c.Item, 80)
		}
		if c.Asking {
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
	for _, p := range data.PRs {
		lines = append(lines, fmt.Sprintf("%s #%d: %s", p.Repo, p.Number, prLine(p)))
	}
	add(contract.ContextPRs, lines, 600, 0)
	if s.maxDepth == 0 || data.Depth < s.maxDepth {
		lines = nil
		for _, p := range s.profiles.List() {
			lines = append(lines, fmt.Sprintf("%s: taste %d, diagnostics %d, intelligence %d, cost %d; max concurrent %d", p.Name, p.Taste, p.Diagnostics, p.Intelligence, p.Cost, p.MaxConcurrent))
		}
		add(contract.ContextProfiles, lines, 1200, 0)
	}
	if s.usage != nil {
		add(contract.ContextUsage, usageLines(data.Accounts, s.usage.Latest(), s.now()), usageChars, 0)
	}
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
