package watch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func savedCheckSummaries(r Record, snap Snapshot) []CheckSummary {
	checks := snap.CheckSummaries(refRecord(r))
	for i := range checks {
		c := &checks[i]
		for _, old := range r.Checks {
			if c.ID > 0 && old.ID == c.ID && old.Completed == c.Completed && old.State == c.State {
				c.FailureExcerpt, c.FailureExcerptChecked = old.FailureExcerpt, old.FailureExcerptChecked
				break
			}
		}
	}
	return checks
}

func failureExcerpt(text string, tail bool) string {
	lines := logLines(Sanitize(text))
	if tail {
		if blocks := failedGoBlocks(lines); len(blocks) > 0 {
			return failureExcerpt(strings.Join(blocks, "\n"), false)
		}
		failure := -1
		// Prefer the test failure over a package summary followed by passing tests.
		for i, line := range lines {
			if strings.Contains(line, "--- FAIL") {
				failure = i
				break
			}
		}
		if failure < 0 {
			for i, line := range lines {
				for _, word := range strings.Fields(line) {
					if word == "FAIL" {
						failure = i
						break
					}
				}
				if failure >= 0 {
					break
				}
			}
		}
		if failure >= 0 {
			start := max(0, failure-3)
			lines = lines[start:min(len(lines), start+30)]
			tail = false
		} else if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
	}
	if !tail && len(lines) > 30 {
		lines = lines[:30]
	}
	text = strings.Join(lines, "\n")
	if len(text) > 4096 {
		if tail {
			text = text[len(text)-4096:]
		} else {
			text = text[:4096]
		}
	}
	return strings.ToValidUTF8(text, "")
}

// ansiColors matches the color and cursor sequences a program writes for a
// terminal: CSI, OSC and the short escape forms.
var ansiColors = regexp.MustCompile("\\x1b\\[[0-9;?]*[ -/]*[@-~]|\\x1b\\][^\\x07\\x1b]*(?:\\x07|\\x1b\\\\)|\\x1b[@-Z\\\\-_]")

// logTimestamp reports whether text starts with the time GitHub writes at the
// start of each log line.
func logTimestamp(text string) bool {
	prefix, _, _ := strings.Cut(text, " ")
	_, err := time.Parse(time.RFC3339Nano, prefix)
	return err == nil
}

// logLines drops what a CI log shows only in a terminal: ANSI colors, the job
// and step columns that gh run view adds, and each line's GitHub timestamp.
func logLines(text string) []string {
	lines := strings.Split(strings.TrimSpace(ansiColors.ReplaceAllString(text, "")), "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if _, rest, ok := strings.Cut(line, "\t"); ok {
			if _, body, ok := strings.Cut(rest, "\t"); ok && logTimestamp(body) {
				line = body
			}
		}
		if _, rest, ok := strings.Cut(line, " "); ok && logTimestamp(line) {
			line = rest
		}
		lines[i] = line
	}
	return lines
}

// logExcerpt is the notice form of a log tail: cleaned lines, and only the
// last ones, where a failure is reported.
func logExcerpt(text string) string {
	lines := logLines(Sanitize(text))
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	text = strings.Join(lines, "\n")
	if len(text) > 4096 {
		text = strings.ToValidUTF8(text[len(text)-4096:], "")
	}
	return text
}

// Go verbose output places the diagnostic between RUN and FAIL. Parent
// failures repeat their failed children, so keep only the specific test blocks.
func failedGoBlocks(lines []string) []string {
	type block struct {
		name       string
		start, end int
	}
	starts := map[string]int{}
	var blocks []block
	for i, line := range lines {
		text := strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(text, "=== RUN "); ok {
			starts[strings.TrimSpace(name)] = i
		}
		if detail, ok := strings.CutPrefix(text, "--- FAIL: "); ok {
			fields := strings.Fields(detail)
			if len(fields) > 0 {
				if start, found := starts[fields[0]]; found {
					blocks = append(blocks, block{fields[0], start, i + 1})
				}
			}
		}
	}
	var out []string
	size := 0
	for _, b := range blocks {
		parent := false
		for _, other := range blocks {
			if strings.HasPrefix(other.name, b.name+"/") {
				parent = true
				break
			}
		}
		if parent {
			continue
		}
		// Keep the RUN label and the end of the block, where Go prints
		// the diagnostic and FAIL marker. Drop unrelated test progress.
		header := clip(lines[b.start], 300)
		var detail []string
		for _, line := range lines[b.start+1 : b.end] {
			text := strings.TrimSpace(line)
			if strings.HasPrefix(text, "=== ") || strings.HasPrefix(text, "--- PASS:") || strings.HasPrefix(text, "--- SKIP:") {
				continue
			}
			detail = append(detail, line)
		}
		if len(detail) > 29 {
			detail = detail[len(detail)-29:]
		}
		text := strings.Join(detail, "\n")
		remaining := 4096 - len(header) - 1
		if len(text) > remaining {
			text = strings.ToValidUTF8(text[len(text)-remaining:], "")
		}
		part := append([]string{header}, strings.Split(text, "\n")...)
		bytes := len(strings.Join(part, "\n"))
		if len(out) > 0 {
			if size+bytes+2 > 4096 || len(out)+len(part)+1 > 30 {
				break
			}
			out = append(out, "")
			size += 2
		}
		out = append(out, part...)
		size += bytes
	}
	return out
}

// Reports are already downloaded by enforceTestBudget. Never fetch them a second time.
func (s *Service) saveCheckFailureExcerpts(ctx context.Context, gh GitHub, r Record, snap Snapshot) error {
	count := 0
	for _, c := range r.Checks {
		isActions := false
		for _, source := range snap.Checks {
			if source.ID == c.ID && source.App.Slug == "github-actions" {
				isActions = true
				break
			}
		}
		if c.State != "failure" || c.ID <= 0 || !isActions {
			continue
		}
		count++
		if count > 5 {
			break
		}
		if c.FailureExcerptChecked {
			continue
		}
		excerpt := ""
		if r.CITestHead == snap.Pull.Head.SHA {
			for _, run := range currentPRRuns(snap) {
				if strings.Contains(c.URL, fmt.Sprintf("/actions/runs/%d/", run.ID)) {
					// A workflow report has no job identity. Use it only when this
					// is the sole check in that run; otherwise each job needs its own log.
					count := 0
					for _, other := range r.Checks {
						if strings.Contains(other.URL, fmt.Sprintf("/actions/runs/%d/", run.ID)) {
							count++
						}
					}
					if count == 1 {
						excerpt = r.CITestFailures[runToken(run)]
					}
					break
				}
			}
		}
		if excerpt == "" {
			jobID := c.ID
			// Actions details URLs carry the job ID, which can differ from the check-run ID.
			if u, parseErr := url.Parse(c.URL); parseErr == nil {
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				for i := 0; i+1 < len(parts); i++ {
					if parts[i] == "job" {
						if id, e := strconv.ParseInt(parts[i+1], 10, 64); e == nil && id > 0 {
							jobID = id
						}
						break
					}
				}
			}
			endpoint := fmt.Sprintf("repos/%s/actions/jobs/%d/logs", r.Repo, jobID)
			raw, _, err := gh.requestBody(ctx, refRecord(r), endpoint, "GET", true)
			if err != nil {
				// Optional logs preserve rate backoff and safe errors from the shared client.
				var rate *rateLimitError
				if errors.As(err, &rate) || testInspectionCanceled(ctx, err) {
					return nil
				}
				excerpt = artifactRequestProblem(r, Run{ID: c.ID}, "job log unavailable", err)
			} else {
				excerpt = failureExcerpt(string(raw), true)
			}
		}
		if err := s.Store.updateRecords([]string{r.Key}, func(st *State) error {
			live := st.Records[r.Key]
			if live == nil || live.Head != snap.Pull.Head.SHA {
				return nil
			}
			for i := range live.Checks {
				check := &live.Checks[i]
				if check.ID == c.ID && check.Completed == c.Completed && check.State == "failure" {
					check.FailureExcerpt, check.FailureExcerptChecked = excerpt, true
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
