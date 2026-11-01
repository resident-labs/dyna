package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
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

func (s *Service) ContextSnapshot(ctx context.Context, caller contract.Principal, req contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ContextSnapshotResponse{}, forbidden()
	}
	switch req.Section {
	case "", contract.ContextChildren, contract.ContextRequests, contract.ContextPRs, contract.ContextProfiles, contract.ContextUsage:
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
	for _, c := range data.Children {
		line := fmt.Sprintf("%s: %s, %s", c.Name, c.Profile, c.State)
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
		approval := "not approved"
		if p.Approved {
			approval = "approved"
		}
		lines = append(lines, fmt.Sprintf("%s #%d: %s", p.Repo, p.Number, approval))
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
