package watch

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/cibudget"
)

const maxTestArtifact = 20 * 1024 * 1024

type testArtifact struct {
	Name    string `json:"name"`
	ID      int64  `json:"id"`
	Size    int64  `json:"size_in_bytes"`
	Created string `json:"created_at"`
	Expired bool   `json:"expired"`
}

// readTestArtifact never extracts files. Only XML bytes read from the archive
// use the shared expansion limit; unrelated entries cannot starve a report.
func readTestArtifact(data []byte, remaining *int64) (cibudget.Report, bool, error) {
	var report cibudget.Report
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return report, false, fmt.Errorf("report unreadable: invalid artifact ZIP")
	}
	if len(archive.File) > 10000 {
		return report, false, fmt.Errorf("report unreadable: artifact has over 10000 entries")
	}
	var problems []error
	junit := false
	for _, file := range archive.File {
		if file.FileInfo().IsDir() || !strings.EqualFold(path.Ext(file.Name), ".xml") {
			continue
		}
		name := boundedTestName(file.Name)
		if file.UncompressedSize64 > uint64(*remaining) {
			problems = append(problems, fmt.Errorf("report unreadable: %s exceeds XML size limit", name))
			continue
		}
		input, err := file.Open()
		if err != nil {
			problems = append(problems, fmt.Errorf("report unreadable: %s", name))
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(input, int64(file.UncompressedSize64)+1))
		input.Close()
		*remaining -= min(int64(len(raw)), *remaining)
		if readErr != nil || uint64(len(raw)) != file.UncompressedSize64 {
			problems = append(problems, fmt.Errorf("report unreadable: %s", name))
			continue
		}
		// XML settings and other well-formed non-JUnit files are ordinary artifacts.
		decoder := xml.NewDecoder(bytes.NewReader(raw))
		root := ""
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			if start, ok := token.(xml.StartElement); ok {
				root = start.Name.Local
				break
			}
		}
		if root != "testsuite" && root != "testsuites" && !bytes.Contains(raw, []byte("<testsuite")) {
			continue
		}
		junit = true
		var part cibudget.Report
		if err := part.ReadJUnit(bytes.NewReader(raw)); err != nil {
			problems = append(problems, fmt.Errorf("report unreadable: %s", name))
			continue
		}
		report.Tests = append(report.Tests, part.Tests...)
		report.Failures = append(report.Failures, part.Failures...)
	}
	return report, junit, errors.Join(problems...)
}

func likelyTestArtifact(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "junit") || strings.Contains(name, "test-report") || strings.Contains(name, "test-results")
}

// likelyRawTestOutput reports an artifact that may hold raw test output. A shard
// that exports no JUnit report leaves this file as its only evidence.
func likelyRawTestOutput(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "test-raw") || strings.Contains(name, "test-log") || strings.Contains(name, "test-output")
}

// artifactRank orders artifacts so the most useful are inspected first: a JUnit
// report, then raw test output, then anything else. The fallback pool holds
// three files, so a bundle must not take a slot from raw test output.
func artifactRank(name string) int {
	switch {
	case likelyTestArtifact(name):
		return 0
	case likelyRawTestOutput(name):
		return 1
	}
	return 2
}

type testArtifactHTTPError struct {
	status int
	retry  bool
}

func (e *testArtifactHTTPError) Error() string {
	return fmt.Sprintf("artifact HTTP status %d", e.status)
}

// artifactGitHub retains safe HTTP status information on the shared command path.
func artifactGitHub(gh GitHub) GitHub {
	gh.artifactErrors = true
	return gh
}

// artifactCommandError classifies responses after account-aware execution.
func artifactCommandError(ctx context.Context, raw []byte, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if response, parseErr := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil); parseErr == nil {
		response.Body.Close()
		if response.StatusCode >= 400 {
			rateLimited := response.StatusCode == 403 && (response.Header.Get("X-Ratelimit-Remaining") == "0" || response.Header.Get("Retry-After") != "" || bytes.Contains(bytes.ToLower(raw), []byte("rate limit")))
			return &testArtifactHTTPError{response.StatusCode, rateLimited}
		}
	}
	return err
}

func testInspectionCanceled(ctx context.Context, err error) bool {
	var timeout net.Error
	var status *testArtifactHTTPError
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &timeout) && timeout.Timeout()) || (errors.As(err, &status) && status.status == 408)
}

func permanentTestRequestError(err error) bool {
	var status *testArtifactHTTPError
	return errors.As(err, &status) && status.status >= 400 && status.status < 500 &&
		status.status != 408 && status.status != 425 && status.status != 429 && !status.retry
}

// A zero count means canceled work; it must still bypass the cheap poll path.
func pendingTestReports(r Record) bool { return len(r.CITestRetries) > 0 }

func resetTestReports(r *Record, head, policy string) {
	if r.CITestHead != head || r.CITestPolicy != policy {
		r.CITestHead, r.CITestPolicy = head, policy
		r.CITestRuns = map[string]string{}
		r.CITestFailures = map[string]string{}
		r.CITestErrors = map[string]string{}
		r.CITestRetries = map[string]int{}
	}
}

// Artifact URLs can contain signed blob credentials. Keep the cause, not the URL.
var artifactErrorURL = regexp.MustCompile(`https?://[^\s]+`)

func artifactRequestProblem(r Record, run Run, stage string, err error) string {
	detail := artifactErrorURL.ReplaceAllString(Sanitize(err.Error()), "[URL]")
	detail = clip(strings.Join(strings.Fields(detail), " "), 500)
	reason := stage + ": " + detail
	log.Printf("PR #%d run %s: %s", r.Number, runToken(run), reason)
	return "report unreadable: " + reason
}

// inspectTestRun isolates optional artifact errors from the main PR lifecycle.
func inspectTestRun(ctx context.Context, gh GitHub, r Record, run Run) (cibudget.Report, bool, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	gh = artifactGitHub(gh)
	var report cibudget.Report
	var pages []struct {
		Artifacts []testArtifact `json:"artifacts"`
	}
	endpoint := fmt.Sprintf("repos/%s/actions/runs/%d/artifacts?per_page=100", r.Repo, run.ID)
	if err := gh.pages(ctx, refRecord(r), endpoint, &pages); err != nil {
		problems := []string{artifactRequestProblem(r, run, "artifact listing failed", err)}
		if permanentTestRequestError(err) && !testInspectionCanceled(ctx, err) {
			return report, false, problems, nil
		}
		return report, false, problems, err
	}
	var artifacts []testArtifact
	for _, page := range pages {
		artifacts = append(artifacts, page.Artifacts...)
	}
	// Prefer report names, then raw test output, but retain bounded discovery
	// for custom artifact names.
	sort.SliceStable(artifacts, func(i, j int) bool {
		return artifactRank(artifacts[i].Name) < artifactRank(artifacts[j].Name)
	})
	remaining := int64(maxTestArtifact)
	fallbackBytes, fallbackCount := int64(maxTestArtifact), 3
	junit := false
	var problems []string
	uninspected, oversized, auxiliary := 0, 0, 0
	for _, artifact := range artifacts {
		if artifact.Expired || artifact.ID <= 0 {
			continue
		}
		// A rerun lists the artifacts of earlier attempts too, so the filter for
		// the current attempt comes first: an old file must not count as skipped
		// or oversized, and it must not take a fallback slot.
		if run.Attempt > 1 {
			started, startErr := time.Parse(time.RFC3339, run.Started)
			created, createErr := time.Parse(time.RFC3339, artifact.Created)
			if startErr != nil || createErr != nil || created.Before(started) {
				continue
			}
		}
		apart := !likelyTestArtifact(artifact.Name)
		if apart {
			auxiliary++
		}
		if artifact.Size < 0 || artifact.Size > maxTestArtifact {
			if apart {
				uninspected++
			} else {
				oversized++
			}
			continue
		}
		if !likelyTestArtifact(artifact.Name) {
			if fallbackCount == 0 || artifact.Size > fallbackBytes {
				uninspected++
				continue
			}
			fallbackCount--
			fallbackBytes -= artifact.Size
		}
		download := gh
		if download.Timeout < 90*time.Second {
			download.Timeout = 90 * time.Second
		}
		endpoint := fmt.Sprintf("repos/%s/actions/artifacts/%d/zip", r.Repo, artifact.ID)
		raw, _, err := download.requestBody(ctx, refRecord(r), endpoint, "GET", true)
		if err != nil {
			problems = append(problems, artifactRequestProblem(r, run, boundedTestName(artifact.Name)+" artifact download failed", err))
			if testInspectionCanceled(ctx, err) || !permanentTestRequestError(err) {
				return report, junit, problems, err
			}
			continue
		}
		if !likelyTestArtifact(artifact.Name) {
			fallbackBytes -= max(int64(len(raw))-artifact.Size, 0)
		}
		part, found, err := readTestArtifact(raw, &remaining)
		if err != nil {
			problems = append(problems, err.Error())
		}
		junit = junit || found
		report.Tests = append(report.Tests, part.Tests...)
		report.Failures = append(report.Failures, part.Failures...)
	}
	if uninspected > 0 {
		// One line holds any number of skipped artifacts: a list per artifact
		// would bury every other report problem in the notice. These are the
		// artifacts that only looked like evidence, not a test report, so the
		// line names them as such: a reader must not think test reports were
		// skipped.
		problems = append(problems, fmt.Sprintf("Other CI artifacts: %d of %d not inspected (the limit is 3 files and 20 MiB per run).", uninspected, auxiliary))
	}
	if oversized > 0 {
		problems = append(problems, fmt.Sprintf("Test reports: %d %s over the 20 MiB size limit.", oversized, reportNoun(oversized)))
	}
	if ctx.Err() != nil {
		return report, junit, problems, ctx.Err()
	}
	return report, junit, problems, nil
}

func (s *Service) enforceTestBudget(ctx context.Context, gh GitHub, r Record, snap Snapshot, policy ChangePolicy) error {
	if snap.Pull.State == "closed" {
		return nil
	}
	max, allowed, err := policy.TestBudget()
	if err != nil {
		return s.Store.update(func(st *State) error {
			live := st.Records[r.Key]
			if live == nil || live.Head != r.Head {
				return nil
			}
			note := "report unreadable: invalid stored test budget"
			live.CITestReportNote = note
			view := *live
			view.Head = snap.Pull.Head.SHA
			notice(&view, live.Author, note, "test-budget-policy:"+digest(policy.CITestMax))
			live.Notices = view.Notices
			return nil
		})
	}
	// Scope names the rule that fails only the PR's own tests, so verdicts
	// saved under an older rule are checked again. Base is part of the key
	// because GitHub lists the PR's files against its current base: a retarget
	// changes them without a new head or CI run.
	policyJSON, _ := json.Marshal(struct {
		Max   string
		Allow []string
		Warn  bool
		Mode  string
		Scope string
		Base  string
	}{max.String(), policy.CITestAllow, policy.WarnNoJUnit(), policy.CITimeBudget, "pr-tests-2", snap.Pull.Base.Ref})
	policyKey := digest(string(policyJSON))
	current := currentPRRuns(snap)
	if pendingTestReports(r) {
		if err := s.Store.update(func(st *State) error {
			live := st.Records[r.Key]
			if live == nil || live.Head != r.Head {
				return nil
			}
			active := map[string]bool{}
			for _, run := range current {
				active[runToken(run)] = true
			}
			for token := range live.CITestRetries {
				if !active[token] {
					delete(live.CITestRetries, token)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	for _, run := range current {
		if run.Event != "pull_request" || run.Status != "completed" {
			continue
		}
		token := runToken(run)
		if r.CITestHead == snap.Pull.Head.SHA && r.CITestPolicy == policyKey && r.CITestRuns[token] != "" {
			continue
		}
		report, junit, problems, inspectErr := inspectTestRun(ctx, gh, r, run)
		if inspectErr != nil {
			// Shutdown cancellation leaves work pending; timeouts consume a retry.
			canceled := errors.Is(ctx.Err(), context.Canceled) || errors.Is(inspectErr, context.Canceled)
			retry := true
			if err := s.Store.update(func(st *State) error {
				live := st.Records[r.Key]
				if live == nil || live.Head != r.Head {
					return nil
				}
				resetTestReports(live, snap.Pull.Head.SHA, policyKey)
				if live.CITestRetries == nil {
					live.CITestRetries = map[string]int{}
				}
				attempts := live.CITestRetries[token]
				if !canceled {
					attempts++
				}
				live.CITestRetries[token] = attempts
				retry = canceled || attempts < 3
				return nil
			}); err != nil {
				return err
			}
			if canceled {
				return nil
			}
			if retry {
				continue
			}
			problems = append(problems, "report unreadable: stopped after 3 failed inspection attempts")
		}
		report.Tests = report.BudgetTests()
		var allowWarnings []string
		if junit {
			allowWarnings = report.AllowWarnings(allowed)
		}
		sort.SliceStable(report.Tests, func(i, j int) bool {
			if report.Tests[i].Duration == report.Tests[j].Duration {
				return report.Tests[i].ID() < report.Tests[j].ID()
			}
			return report.Tests[i].Duration > report.Tests[j].Duration
		})
		var slow []cibudget.Test
		seen := map[string]bool{}
		for _, test := range report.Tests {
			if test.Duration > max && !test.Allowed(allowed) && !seen[test.ID()] {
				slow = append(slow, test)
				seen[test.ID()] = true
			}
		}
		// Every inspected run fills the CI health rows, so the view never reads
		// a report itself. The breached names drive the quiet crossing line.
		breached := s.recordTestHealth(ctx, r, run, report.Tests, max)
		// Only tests the PR adds or changes fail it. Other slow tests are warnings.
		var names, warned []string
		var files []prFile
		var filesErr error
		if len(slow) > 0 && r.Number > 0 {
			files, filesErr = gh.prTestFiles(ctx, r, snap.Pull.Head.SHA)
		}
		// A failed file-list read retries on later ticks. After the retry
		// limit, the slow tests are warnings: a failed read proves nothing.
		if filesErr != nil {
			retry := true
			if err := s.Store.update(func(st *State) error {
				live := st.Records[r.Key]
				if live == nil || live.Head != r.Head {
					return nil
				}
				resetTestReports(live, snap.Pull.Head.SHA, policyKey)
				if live.CITestRetries == nil {
					live.CITestRetries = map[string]int{}
				}
				live.CITestRetries[token]++
				retry = live.CITestRetries[token] < 3
				return nil
			}); err != nil {
				return err
			}
			if retry && !testInspectionCanceled(ctx, filesErr) {
				continue
			}
			if testInspectionCanceled(ctx, filesErr) {
				return nil
			}
		}
		for _, test := range slow {
			if r.Number == 0 || (filesErr == nil && changedByPR(test, files)) {
				names = append(names, boundedTestName(test.ID()))
			} else {
				warned = append(warned, boundedTestName(test.ID()))
			}
		}
		reason := "checked"
		if len(problems) > 0 {
			reason = clip(strings.Join(problems, "; "), 3000)
		}
		if len(names) == 0 && len(warned) > 0 {
			reason = "warning: tests over " + compactDuration(max) + " outside this PR: " + strings.Join(boundedNames(warned), ", ")
		}
		var text string
		if len(names) > 0 {
			// Keep PR state and wake payloads bounded even for large reports.
			names = boundedNames(names)
			reason = fmt.Sprintf("tests over %s: %s", compactDuration(max), strings.Join(names, ", "))
			if len(warned) > 0 {
				reason += "; warning, outside this PR: " + strings.Join(boundedNames(warned), ", ")
			}
			output := boundedio.New(10000)
			report.Write(output, max, allowed)
			outcome := "failed"
			if !policy.EnforceCITimeBudget() {
				outcome = "warning"
				reason = "warning: " + reason
			}
			text = fmt.Sprintf("CI for %s %s: %s.\n%s", r.URL, outcome, reason, output.Bytes())
			if len(text) > 12000 {
				text = strings.ToValidUTF8(text[:12000], "")
			}
		}
		if err := s.Store.update(func(st *State) error {
			live := st.Records[r.Key]
			if live == nil || live.Head != r.Head {
				return nil
			}
			resetTestReports(live, snap.Pull.Head.SHA, policyKey)
			delete(live.CITestRetries, token)
			if live.CITestRuns == nil {
				live.CITestRuns = map[string]string{}
			}
			live.CITestRuns[token] = reason
			if live.CITestFailures == nil {
				live.CITestFailures = map[string]string{}
			}
			var failures []string
			for _, failure := range report.Failures {
				lines := strings.Split(failure, "\n")
				failures = append(failures, strings.Join(lines[:min(len(lines), 6)], "\n"))
			}
			live.CITestFailures[token] = failureExcerpt(strings.Join(failures, "\n\n"), false)
			if len(problems) > 0 {
				detail := clip(strings.Join(problems, "; "), 3000)
				if live.CITestErrors == nil {
					live.CITestErrors = map[string]string{}
				}
				live.CITestErrors[token] = detail
				view := *live
				view.Head = snap.Pull.Head.SHA
				notice(&view, live.Author, detail, "test-report-error:"+snap.Pull.Head.SHA+":"+token)
				live.Notices = view.Notices
			}
			warnNoJUnit := policy.WarnNoJUnit() && run.Conclusion != "cancelled" && !onlySkippedOrCancelledJobs(snap.Checks)
			if len(problems) == 0 && (junit || !warnNoJUnit) {
				live.CITestReportNote = ""
			}
			if !junit && len(problems) == 0 && warnNoJUnit && !live.CINoJUnitWarned {
				note := fmt.Sprintf("CI uploaded no JUnit XML test report, so per-test timing (%s budget) is not enforced. Upload a JUnit report as an artifact.", strconv.FormatFloat(max.Seconds(), 'f', -1, 64)+" s")
				live.CINoJUnitWarned = true
				live.CITestReportNote = note
				view := *live
				view.Head = snap.Pull.Head.SHA
				notice(&view, live.Author, note, "no-junit-report")
				live.Notices = view.Notices
			}
			var unreadable []string
			for _, current := range currentPRRuns(snap) {
				if detail := live.CITestErrors[runToken(current)]; detail != "" {
					unreadable = append(unreadable, fmt.Sprintf("Run %s: %s", runToken(current), detail))
				}
			}
			if len(unreadable) > 0 {
				live.CITestReportNote = clip(strings.Join(unreadable, "; "), 5000)
			}
			if len(allowWarnings) > 0 {
				live.CITestReportNote = clip(strings.TrimSpace(live.CITestReportNote+"\n"+strings.Join(allowWarnings, "\n")), 5000)
			}
			if text != "" {
				view := *live
				view.Head = snap.Pull.Head.SHA
				prefix := "test-budget:"
				if !policy.EnforceCITimeBudget() {
					prefix = "informational:" + prefix
				}
				notice(&view, live.Author, text, prefix+snap.Pull.Head.SHA+":"+token)
				live.Notices = view.Notices
			}
			return nil
		}); err != nil {
			return err
		}
		s.postSlowTestCrossings(ctx, r, run, breached)
	}
	return nil
}

func boundedNames(names []string) []string {
	if len(names) > 10 {
		return append(names[:10:10], fmt.Sprintf("and %d more", len(names)-10))
	}
	return names
}

// reportNoun keeps a collapsed report line readable at one artifact.
func reportNoun(n int) string {
	if n == 1 {
		return "artifact"
	}
	return "artifacts"
}

func boundedTestName(name string) string {
	name = Sanitize(name)
	if len(name) > 300 {
		return strings.ToValidUTF8(name[:300], "") + "…"
	}
	return name
}

// currentPRRuns keeps a push run from hiding the pull request report for the same workflow.
func currentPRRuns(snap Snapshot) []Run {
	var prs []Run
	for _, run := range snap.Runs {
		// The endpoint is head-scoped; reject any explicit mismatch before
		// choosing the latest workflow, so an old run cannot hide a current one.
		if run.HeadSHA != "" && run.HeadSHA != snap.Pull.Head.SHA {
			continue
		}
		if run.Event == "pull_request" {
			prs = append(prs, run)
		}
	}
	return currentRuns(prs)
}

// onlySkippedOrCancelledJobs excludes the "changes" filter. Skipped and
// cancelled jobs do not need to upload a test report.
func onlySkippedOrCancelledJobs(checks []Check) bool {
	found := false
	for _, c := range checks {
		if c.App.Slug != "github-actions" || c.Name == "changes" {
			continue
		}
		if c.Conclusion != "skipped" && c.Conclusion != "cancelled" {
			return false
		}
		found = true
	}
	return found
}
