package watch

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"dyna-agent/internal/ciexcerpt"
)

// failureExcerpt is the sanitized failing part of a log text. See ciexcerpt.Failure.
func failureExcerpt(text string, tail bool) string {
	return ciexcerpt.Failure(Sanitize(text), tail)
}

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

// logExcerpt is the notice form of a log tail: cleaned lines, and only the
// last ones, where a failure is reported.
func logExcerpt(text string) string {
	lines := ciexcerpt.Lines(Sanitize(text))
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	text = strings.Join(lines, "\n")
	if len(text) > 4096 {
		text = strings.ToValidUTF8(text[len(text)-4096:], "")
	}
	return text
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
