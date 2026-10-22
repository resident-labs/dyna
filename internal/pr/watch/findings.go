package watch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"dyna-agent/internal/pr/state"
)

const MaxFindingsBytes = 32000

var findingIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func cloneFindings(in []Finding) []Finding {
	out := make([]Finding, len(in))
	for i, f := range in {
		out[i] = f
		out[i].Replies = append([]state.FindingReply{}, f.Replies...)
	}
	return out
}
func prepareFindings(r Record, verdict string, input []Finding) ([]Finding, error) {
	if len(input) > 100 {
		return nil, fmt.Errorf("a review can contain at most 100 findings")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFindingsBytes {
		return nil, fmt.Errorf("structured findings exceed %d bytes", MaxFindingsBytes)
	}
	out := cloneFindings(input)
	seen := map[string]bool{}
	for i := range out {
		f := &out[i]
		switch f.Severity {
		case "blocking", "should-fix", "nit":
		default:
			return nil, fmt.Errorf("finding severity must be blocking, should-fix, or nit")
		}
		if approved(verdict) && f.Severity != "nit" {
			return nil, fmt.Errorf("approval cannot contain blocking or should-fix findings")
		}
		if f.File == "" || f.Line < 0 || strings.TrimSpace(f.Why) == "" || strings.TrimSpace(f.Fix) == "" {
			return nil, fmt.Errorf("each finding needs file, nonnegative line, why, and fix")
		}
		if f.ID == "" {
			f.ID = fmt.Sprintf("f%d-%d", r.Round, i+1)
		}
		if !findingIDPattern.MatchString(f.ID) || seen[f.ID] {
			return nil, fmt.Errorf("finding IDs must be unique labels of 1 to 64 characters")
		}
		seen[f.ID] = true
		f.File, f.Why, f.Fix, f.Patch = Sanitize(f.File), Sanitize(f.Why), Sanitize(f.Fix), Sanitize(f.Patch)
		// Replies come only from the author reply endpoint, not reviewer input.
		f.Replies = []state.FindingReply{}
		for _, old := range r.Findings {
			if old.ID == f.ID {
				f.Replies = append(f.Replies, old.Replies...)
				break
			}
		}
	}
	weight := map[string]int{"blocking": 0, "should-fix": 1, "nit": 2}
	sort.SliceStable(out, func(i, j int) bool { return weight[out[i].Severity] < weight[out[j].Severity] })
	return out, nil
}
