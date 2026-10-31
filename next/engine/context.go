package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
)

func renderContextSection(key string, lines []string, cap int, full bool, rowLimit int) contract.ContextSection {
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
		if chars+sep+n+utf8.RuneCountInString(suffix) > cap {
			break
		}
		kept = append(kept, line)
		chars += sep + n
	}
	if more := len(lines) - len(kept); more > 0 {
		suffix := fmt.Sprintf("%d more: context show", more)
		for len(kept) > 0 && utf8.RuneCountInString(strings.Join(append(kept, suffix), "\n")) > cap {
			kept = kept[:len(kept)-1]
			more++
			suffix = fmt.Sprintf("%d more: context show", more)
		}
		kept = append(kept, suffix)
	}
	return contract.ContextSection{Key: key, Text: strings.Join(kept, "\n")}
}

func (s *Service) ContextSnapshot(ctx context.Context, caller contract.Principal, req contract.ContextSnapshotRequest) (contract.ContextSnapshotResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ContextSnapshotResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ContextSnapshotResponse{}, failure(err)
	}
	if req.Section != "" && req.Section != "dyna_goal" && req.Section != "dyna_children" && req.Section != "dyna_requests" && req.Section != "dyna_prs" && req.Section != "dyna_profiles" && req.Section != "dyna_usage" {
		return contract.ContextSnapshotResponse{}, invalid("unknown context section")
	}
	result := contract.ContextSnapshotResponse{Sections: []contract.ContextSection{}}
	add := func(key string, lines []string, cap, limit int) {
		if len(lines) > 0 && (req.Section == "" || req.Section == key) {
			result.Sections = append(result.Sections, renderContextSection(key, lines, cap, req.Section != "", limit))
		}
	}
	children, err := s.store.ContextChildren(ctx, session.ID)
	if err != nil {
		return result, failure(err)
	}
	var lines []string
	for _, c := range children {
		lines = append(lines, fmt.Sprintf("%s: %s, %s", c.Name, c.Profile, c.State))
	}
	add("dyna_children", lines, 1000, 8)
	requests, err := s.store.ContextRequests(ctx, session.ID)
	if err != nil {
		return result, failure(err)
	}
	lines = nil
	for _, r := range requests {
		lines = append(lines, fmt.Sprintf("%s: %s, waiting", r.ID, r.Kind))
	}
	add("dyna_requests", lines, 600, 0)
	prs, err := s.store.ContextPRs(ctx, session.ID)
	if err != nil {
		return result, failure(err)
	}
	lines = nil
	for _, p := range prs {
		approval := "not approved"
		if p.Approved {
			approval = "approved"
		}
		lines = append(lines, fmt.Sprintf("%s #%d: %s", p.Repo, p.Number, approval))
	}
	add("dyna_prs", lines, 600, 0)
	depth, err := s.store.ContextDepth(ctx, session.ID)
	if err != nil {
		return result, failure(err)
	}
	if s.maxDepth == 0 || depth < s.maxDepth {
		lines = nil
		for _, p := range s.profiles.List() {
			lines = append(lines, fmt.Sprintf("%s: taste %d, diagnostics %d, intelligence %d, cost %d; max concurrent %d, max calls %d", p.Name, p.Taste, p.Diagnostics, p.Intelligence, p.Cost, p.MaxConcurrent, p.MaxCallsPerRun))
		}
		add("dyna_profiles", lines, 1200, 0)
	}
	return result, nil
}
