package shells

import (
	"regexp"
	"slices"
	"strings"

	"github.com/zricethezav/gitleaks/v8/detect"
)

// Redacted replaces a secret in shown output. The command itself still gets
// the real environment; only the text written to output.log is masked.
const Redacted = "[REDACTED]"

// secretName matches environment variables that hold credentials.
var secretName = regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|PRIVATE_KEY|CREDENTIAL)`)

// pemBegin and pemEnd delimit a private key block, such as an SSH or TLS key.
var (
	pemBegin = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
	pemEnd   = regexp.MustCompile(`-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
)

// minSecretLen keeps short values such as "1" or "true" from masking
// ordinary output.
const minSecretLen = 8

// secretValues returns the exact values of env's secret variables, longest
// first, so a value that contains another is masked whole. Output is masked
// one line at a time, so a multiline value adds each of its lines of
// minSecretLen or more here, and its full line list to multi.
func secretValues(env []string) (values []string, multi [][]string) {
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || len(value) < minSecretLen || !secretName.MatchString(name) {
			continue
		}
		parts := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
		for i, part := range parts {
			// PEM delimiters are not secret. Masked, they would hide another
			// key's BEGIN line and stop its body from being masked.
			if pemBegin.MatchString(part) || pemEnd.MatchString(part) {
				parts[i] = ""
				continue
			}
			if len(part) >= minSecretLen && !slices.Contains(values, part) {
				values = append(values, part)
			}
		}
		if len(parts) > 1 {
			multi = append(multi, parts)
		}
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	return values, multi
}

// redactor masks one job's output, line by line: the exact values of the
// job's secret variables, the body of any private key block, and any key
// gitleaks' default rules find.
type redactor struct {
	values []string
	// multi holds the lines of multiline secrets. A short line, such as a
	// PEM key's last base64 line, is too common to match on its own, so it
	// is masked where the secret continues: on the line after one that ended
	// with the secret's previous line.
	multi [][]string
	next  []string         // secret lines expected on this line
	leaks *detect.Detector // nil when gitleaks' rules fail to load
	inPEM bool             // inside a private key block
}

func newRedactor(env []string) *redactor {
	d, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		d = nil
	} else {
		// A source comment that silences gitleaks must not unmask output.
		d.IgnoreGitleaksAllow = true
	}
	values, multi := secretValues(env)
	return &redactor{values: values, multi: multi, leaks: d}
}

// line masks every secret in one output line. Each source marks spans of the
// original line, and overlapping spans merge before any text is replaced, so
// masking one secret never hides part of another from its own match.
func (r *redactor) line(line string) string {
	eol := eolOf(line)
	body := strings.TrimSuffix(line, eol)
	var spans [][2]int
	all := func(sub string) {
		for at := 0; sub != ""; {
			k := strings.Index(body[at:], sub)
			if k < 0 {
				return
			}
			spans = append(spans, [2]int{at + k, at + k + len(sub)})
			at += k + len(sub)
		}
	}
	for _, v := range r.values {
		all(v)
	}
	// A short line of a multiline secret is too common to match everywhere:
	// mask it on the line after one that ended with the secret's previous
	// line, alone on a line, or as the first line printed as NAME=value.
	expected := r.next
	r.next = nil
	for _, parts := range r.multi {
		for i, part := range parts {
			if part == "" {
				continue
			}
			if i+1 < len(parts) && strings.HasSuffix(body, part) {
				r.next = append(r.next, parts[i+1])
			}
			if body == part || (i == 0 && strings.HasSuffix(body, "="+part)) {
				spans = append(spans, [2]int{len(body) - len(part), len(body)})
			}
		}
	}
	for _, part := range expected {
		all(part)
	}
	// Key material can share a line with its delimiters, so mask text after
	// BEGIN and before END as well as every line between them.
	begin, end := pemBegin.FindStringIndex(body), pemEnd.FindStringIndex(body)
	switch {
	case r.inPEM && end == nil:
		spans = append(spans, [2]int{0, len(body)})
	case r.inPEM || (begin == nil && end != nil):
		// The END of the block, also one whose BEGIN the output never showed.
		r.inPEM = false
		spans = append(spans, [2]int{0, end[0]})
	case begin != nil && (end == nil || end[0] < begin[1]):
		r.inPEM = true
		spans = append(spans, [2]int{begin[1], len(body)})
	case begin != nil:
		spans = append(spans, [2]int{begin[1], end[0]})
	}
	if r.leaks != nil {
		for _, f := range r.leaks.DetectString(body) {
			if len(f.Secret) >= minSecretLen {
				all(f.Secret)
			}
		}
	}
	return maskSpans(body, spans) + eol
}

// maskSpans replaces each run of overlapping or touching spans that holds
// text other than spaces with one Redacted.
func maskSpans(s string, spans [][2]int) string {
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	var out strings.Builder
	at := 0
	for i := 0; i < len(spans); {
		start, stop := spans[i][0], spans[i][1]
		for i++; i < len(spans) && spans[i][0] <= stop; i++ {
			stop = max(stop, spans[i][1])
		}
		if start < at || strings.TrimSpace(s[start:stop]) == "" {
			continue
		}
		out.WriteString(s[at:start])
		out.WriteString(Redacted)
		at = stop
	}
	out.WriteString(s[at:])
	return out.String()
}

// eolOf returns the line's ending, "\n" or "\r\n", or "".
func eolOf(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}
