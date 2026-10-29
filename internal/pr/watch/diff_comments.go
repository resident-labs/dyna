package watch

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/pr/state"
)

// DiffComment is one owner comment on a diff line. Quote is the line with its +, - or space prefix.
type DiffComment struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Quote string `json:"quote"`
	Text  string `json:"text"`
}

// FormatDiffComments is the one text form of owner comments, for the author and for plain sessions.
func FormatDiffComments(comments []DiffComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The owner left %d comment(s) on your diff:\n", len(comments))
	for _, c := range comments {
		fmt.Fprintf(&b, "\n%s:%d\n", c.File, c.Line)
		if c.Quote != "" {
			fmt.Fprintf(&b, "> %s\n", c.Quote)
		}
		fmt.Fprintf(&b, "%s\n", c.Text)
	}
	return b.String()
}

// SendDiffComments stores each comment as an open owner finding and sends one notice to the author.
// The author answers with `dyna git change reply`.
func (s *Service) SendDiffComments(ctx context.Context, key string, comments []DiffComment) error {
	if len(comments) == 0 || len(comments) > 100 {
		return fmt.Errorf("send 1 to 100 comments")
	}
	clean := make([]DiffComment, len(comments))
	for i, c := range comments {
		c.File, c.Quote, c.Text = Sanitize(c.File), Sanitize(c.Quote), Sanitize(strings.TrimSpace(c.Text))
		if c.File == "" || c.Line < 0 || c.Text == "" || len(c.Text) > 4000 {
			return fmt.Errorf("each comment needs a file, a line and 1 to 4000 bytes of text")
		}
		clean[i] = c
	}
	return s.Store.update(func(st *State) error {
		r := recordByID(st, key)
		if r == nil {
			return fmt.Errorf("change is not tracked")
		}
		if !r.Watching || r.State != state.PROpen {
			return fmt.Errorf("change is not open and watched")
		}
		first := len(r.Findings)
		for _, c := range clean {
			r.Findings = append(r.Findings, state.Finding{ID: fmt.Sprintf("o%d", ownerCount(r)+1), Severity: "owner", File: c.File, Line: c.Line, Why: c.Text, Patch: c.Quote, Replies: []state.FindingReply{}})
		}
		text := FormatDiffComments(clean) + "\nReply to each with `dyna git change reply " + changeID(*r) + " <id> fixed|wont-fix|question`. IDs: "
		var ids []string
		for _, f := range r.Findings[first:] {
			ids = append(ids, f.ID)
		}
		notice(r, r.Author, text+strings.Join(ids, ", "), fmt.Sprintf("owner-comments:%s:%d", r.Head, first))
		return nil
	})
}

func ownerCount(r *Record) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == "owner" {
			n++
		}
	}
	return n
}

// ownerFindingsOf keeps owner comments across review rounds, which replace reviewer findings.
func ownerFindingsOf(in []Finding) []Finding { return filterFindings(in, true) }
func filterFindings(in []Finding, owner bool) []Finding {
	out := []Finding{}
	for _, f := range in {
		if (f.Severity == "owner") == owner {
			out = append(out, f)
		}
	}
	return out
}
