package state

import (
	"fmt"
	"strings"
)

// Label is the short reference people type: D-5 for a room issue, #207 for GitHub.
func (r IssueRef) Label() string {
	if r.Kind == IssueKindDyna {
		return fmt.Sprintf("D-%d", r.Number)
	}
	return fmt.Sprintf("#%d", r.Number)
}

// Valid reports whether a ref can name a real issue.
func (r IssueRef) Valid() bool {
	return r.Number > 0 && (r.Kind == IssueKindDyna || r.Kind == IssueKindGitHub)
}

// FixesLabel writes a link list as people type it: "D-5, #207".
func FixesLabel(refs []IssueRef) string {
	labels := make([]string, 0, len(refs))
	for _, ref := range refs {
		labels = append(labels, ref.Label())
	}
	return strings.Join(labels, ", ")
}

// NormalizeFixes removes duplicates and invalid refs, keeping the caller's order.
func NormalizeFixes(refs []IssueRef) []IssueRef {
	seen := make(map[IssueRef]bool, len(refs))
	out := make([]IssueRef, 0, len(refs))
	for _, ref := range refs {
		if !ref.Valid() || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MergeFixes joins two link lists without duplicates, keeping their order.
func MergeFixes(base, extra []IssueRef) []IssueRef {
	return NormalizeFixes(append(append([]IssueRef{}, base...), extra...))
}

// MaxFixes bounds the issues one change may close, so a long or hostile body
// cannot close an unbounded list.
const MaxFixes = 20

// ParseIssueRefs reads a comma or space separated list such as "D-5,#207".
// A bare number is refused because it could name either tracker.
func ParseIssueRefs(text string) ([]IssueRef, error) {
	var out []IssueRef
	for _, token := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		ref, ok := parseIssueRef(token)
		if !ok {
			return nil, fmt.Errorf("use issue references such as D-5 or #207, not %q", token)
		}
		out = append(out, ref)
	}
	if len(NormalizeFixes(out)) > MaxFixes {
		return nil, fmt.Errorf("link at most %d issues", MaxFixes)
	}
	return NormalizeFixes(out), nil
}

// ParseDynaIssueNumbers reads goal --fixes values. Goals link room issues,
// not GitHub issues, so every reference must use the D-N form.
func ParseDynaIssueNumbers(text string) ([]int, error) {
	refs, err := ParseIssueRefs(text)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind != IssueKindDyna {
			return nil, fmt.Errorf("goal --fixes accepts room issues such as D-5, not %s", ref.Label())
		}
		out = append(out, ref.Number)
	}
	return out, nil
}

// ParseFixLines reads the "Fixes D-5" and "Fixes #207" lines of a PR body.
// Prose that names no issue is ignored, and one line may name several issues.
// Only the list of references right after the keyword counts: it ends at the
// first word that is not a reference, so "Closes D-5 on merge. This does not
// close D-6." links D-5 alone.
func ParseFixLines(body string) []IssueRef {
	var out []IssueRef
	for _, line := range strings.Split(body, "\n") {
		rest, ok := fixLine(line)
		if !ok {
			continue
		}
		for _, token := range strings.FieldsFunc(rest, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			if strings.EqualFold(token, "and") || token == "&" {
				continue
			}
			// Sentence punctuation follows a reference in prose: "Fixes D-5."
			ref, ok := parseIssueRef(strings.Trim(token, "()[]{}.,;:!?\"'\u201c\u201d\u2018\u2019"))
			if !ok {
				break
			}
			out = append(out, ref)
			// A reference that ends a sentence ends the list.
			if strings.HasSuffix(token, ".") {
				break
			}
		}
	}
	out = NormalizeFixes(out)
	if len(out) > MaxFixes {
		out = out[:MaxFixes]
	}
	return out
}

// fixLine returns the text after a leading "Fixes"-style keyword, ignoring
// bullet and quote markers in front of it.
func fixLine(line string) (string, bool) {
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*>\u2022"))
	for _, keyword := range []string{"fixes", "fixed", "closes", "closed", "resolves", "resolved"} {
		if len(line) <= len(keyword) || !strings.EqualFold(line[:len(keyword)], keyword) {
			continue
		}
		rest := line[len(keyword):]
		if rest == "" || (rest[0] != ':' && rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":")), true
	}
	return "", false
}

// parseIssueRef reads one typed reference: D-5 or #207.
func parseIssueRef(token string) (IssueRef, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return IssueRef{}, false
	}
	if number, ok := dynaIssueNumber(token); ok {
		return IssueRef{Kind: IssueKindDyna, Number: number}, true
	}
	if number, ok := githubIssueNumber(token); ok {
		return IssueRef{Kind: IssueKindGitHub, Number: number}, true
	}
	return IssueRef{}, false
}

// dynaIssueNumber accepts D-5, d5 and D5.
func dynaIssueNumber(token string) (int, bool) {
	if token == "" || (token[0] != 'd' && token[0] != 'D') {
		return 0, false
	}
	digits := strings.TrimPrefix(token[1:], "-")
	number, ok := digitsNumber(digits)
	return number, ok
}

// githubIssueNumber requires the # so a bare number stays unambiguous.
func githubIssueNumber(token string) (int, bool) {
	if !strings.HasPrefix(token, "#") {
		return 0, false
	}
	return digitsNumber(token[1:])
}

func digitsNumber(digits string) (int, bool) {
	if digits == "" {
		return 0, false
	}
	number := 0
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		number = number*10 + int(digit-'0')
		if number > 1_000_000_000 {
			return 0, false
		}
	}
	if number < 1 {
		return 0, false
	}
	return number, true
}
