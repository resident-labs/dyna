package watch

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"dyna-agent/internal/pr/state"
	"golang.org/x/mod/modfile"
)

var mergeFailedPackage = regexp.MustCompile(`(?:^|\s)FAIL[ \t]+([^\s]+)[ \t]+(?:[0-9.]+s|\[build failed\])`)
var mergeFailedTest = regexp.MustCompile(`--- FAIL:[ \t]+([^\s]+)`)

// mergeFailureReport uses frozen inputs and reads source as data. Missing
// evidence must not prevent the owner from receiving the failure report. The ci
// read names the failed checks when the saved logs carry no job of their own.
func (s *Service) mergeFailureReport(ctx context.Context, r Record, b state.MergeTrial, ci trialCI) (string, error) {
	st, err := s.Store.Read()
	if err != nil {
		return "", err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Batch trial %s failed.\nTested commit: %s\nFrozen PR order:\n", boundedTestName(b.Branch), boundedTestName(b.Head))
	labels := map[string]string{}
	for i, c := range b.Changes {
		label := boundedTestName(c.Key)
		url := "URL unknown"
		if member := st.Records[c.Key]; member != nil {
			label = fmt.Sprintf("#%d", member.Number)
			if member.URL != "" {
				url = boundedTestName(member.URL)
			} else if member.Host != "" && member.Repo != "" {
				url = refRecord(*member).URL()
			}
		}
		labels[c.Key] = label
		fmt.Fprintf(&out, "%d. %s %s at %s\n", i+1, label, url, boundedTestName(c.Head))
	}
	packages, tests, frontend, logJobs := mergeFailureLines(b.Logs)
	jobs, links := s.mergeFailureJobs(ctx, r, b)
	if len(jobs) == 0 && len(ci.Failed) > 0 {
		jobs = append(jobs, ci.Failed...)
		links = append(links, fmt.Sprintf("https://%s/%s/commit/%s/checks", r.Host, r.Repo, b.Head))
	}
	if len(jobs) == 0 {
		jobs = logJobs
	}
	out.WriteString("Failed jobs:\n")
	if len(jobs) == 0 {
		// Only an observed cancellation is an infrastructure cause. A failure
		// that named no job keeps an unknown cause, which is not evidence of
		// anything else.
		if ci.Cancelled {
			out.WriteString("- none: CI for this commit was cancelled, so it never tested the change.\n")
		} else {
			out.WriteString("- unknown; job evidence unavailable.\n")
		}
	}
	for _, job := range jobs {
		fmt.Fprintf(&out, "- %s\n", job)
	}
	logLink := "GitHub log link unknown"
	if len(links) > 0 {
		logLink = strings.Join(links, ", ")
	}
	fmt.Fprintf(&out, "Failure details (%s):\n", logLink)
	for _, pkg := range packages {
		fmt.Fprintf(&out, "- Go package: %s\n", boundedTestName(pkg))
	}
	for _, test := range tests {
		fmt.Fprintf(&out, "- Go test: %s\n", boundedTestName(test))
	}
	for _, line := range frontend {
		fmt.Fprintf(&out, "- Frontend: %s\n", boundedTestName(line))
	}
	if len(packages)+len(tests)+len(frontend) == 0 {
		out.WriteString("- unknown; saved logs contain no recognized test failure.\n")
	}
	out.WriteString("Culprit guess: ")
	candidates := s.mergeFailureCandidates(ctx, r, b, packages, labels)
	if len(candidates) == 0 {
		out.WriteString("unknown; no reliable changed-path match is available.\n")
	} else {
		out.WriteString("possible candidates from changed paths; this is not proof of cause.\n")
		for _, candidate := range candidates {
			fmt.Fprintf(&out, "- %s\n", candidate)
		}
	}
	report := Sanitize(out.String())
	if len(report) > 16000 {
		report = strings.ToValidUTF8(report[:15900], "") + "\nReport truncated; inspect the GitHub logs for full details.\n"
	}
	return report, nil
}

// Go and frontend failures are kept separately: a frontend file is never
// interpreted as a Go package for culprit attribution.
func mergeFailureLines(logs string) (packages, tests, frontend, jobs []string) {
	add := func(dst *[]string, value string) {
		if len(*dst) < 20 && !slices.Contains(*dst, value) {
			*dst = append(*dst, value)
		}
	}
	for _, line := range strings.Split(logs, "\n") {
		failed := false
		if m := mergeFailedPackage.FindStringSubmatch(line); len(m) > 1 {
			add(&packages, m[1])
			failed = true
		}
		if m := mergeFailedTest.FindStringSubmatch(line); len(m) > 1 {
			add(&tests, m[1])
			failed = true
		}
		if (strings.Contains(line, "FAIL ") || strings.Contains(line, "× ") || strings.Contains(line, "✕ ")) && (strings.Contains(line, ".test.") || strings.Contains(line, ".spec.") || strings.Contains(line, " > ")) {
			parts := strings.SplitN(line, "\t", 3)
			add(&frontend, strings.TrimSpace(parts[len(parts)-1]))
			failed = true
		}
		if failed {
			if job, _, ok := strings.Cut(line, "\t"); ok && !strings.Contains(job, "FAIL") {
				add(&jobs, boundedTestName(job))
			}
		}
	}
	return
}

func (s *Service) mergeFailureJobs(ctx context.Context, r Record, b state.MergeTrial) (jobs, links []string) {
	if b.Head == "" || r.Host == "" || r.Repo == "" {
		return
	}
	g := s.github(r.RoomID)
	var snap Snapshot
	if err := g.readCommitCI(ctx, refRecord(r), b.Head, &snap); err != nil {
		return
	}
	var runs []Run
	for _, run := range snap.Runs {
		if run.Event == "push" && run.HeadSHA == b.Head && run.HeadBranch == b.Branch {
			runs = append(runs, run)
		}
	}
	for _, run := range currentRuns(runs) {
		if run.Status != "completed" || run.Conclusion == "" || success(run.Conclusion) || run.Conclusion == "cancelled" {
			continue
		}
		link := fmt.Sprintf("https://%s/%s/actions/runs/%d", r.Host, r.Repo, run.ID)
		if run.Attempt > 0 {
			link += fmt.Sprintf("/attempts/%d", run.Attempt)
		}
		if len(links) < 8 {
			links = append(links, link)
		}
		details, err := g.runJobs(ctx, refRecord(r), run)
		if err != nil {
			continue
		}
		for _, job := range details {
			if job.Status == "completed" && job.Conclusion != "" && !success(job.Conclusion) && job.Conclusion != "cancelled" && len(jobs) < 20 {
				jobs = append(jobs, fmt.Sprintf("%s (%s)", boundedTestName(job.Name), link))
			}
		}
	}
	return
}

func (s *Service) mergeFailureCandidates(ctx context.Context, r Record, b state.MergeTrial, packages []string, labels map[string]string) []string {
	if len(packages) == 0 {
		return nil
	}
	root := filepath.Join(s.Store.Root, "trials")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil
	}
	dir, err := os.MkdirTemp(root, "failure-report-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	if err := s.trialClient(r).PrepareReview(ctx, trialBatch(r, b), dir); err != nil {
		return nil
	}
	base := filepath.Join(dir, "base")
	moduleData, err := os.ReadFile(filepath.Join(base, "go.mod"))
	if err != nil {
		return nil
	}
	module := modfile.ModulePath(moduleData)
	if module == "" {
		return nil
	}
	// Read imports with the Go parser. Do not invoke go list, tests, or tools
	// from the repository. These are direct imports from the frozen base only.
	related := map[string]string{}
	for _, pkg := range packages {
		if pkg != module && !strings.HasPrefix(pkg, module+"/") {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(pkg, module), "/")
		if !mergeReportSafePath(rel) && rel != "" {
			continue
		}
		related[path.Clean(rel)] = "failing package " + pkg
		entries, err := os.ReadDir(filepath.Join(base, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			source, err := parser.ParseFile(token.NewFileSet(), filepath.Join(base, filepath.FromSlash(rel), entry.Name()), nil, parser.ImportsOnly)
			if err != nil {
				continue
			}
			for _, spec := range source.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil || (imported != module && !strings.HasPrefix(imported, module+"/")) {
					continue
				}
				importDir := strings.TrimPrefix(strings.TrimPrefix(imported, module), "/")
				if !mergeReportSafePath(importDir) && importDir != "" {
					continue
				}
				if _, ok := related[path.Clean(importDir)]; !ok {
					related[path.Clean(importDir)] = "direct import " + imported + " of " + pkg
				}
			}
		}
	}
	var candidates []string
	for i, c := range b.Changes {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("pr-%d.patch", i+1)))
		if err != nil {
			continue
		}
		matches := map[string]string{}
		for _, line := range strings.Split(string(data), "\n") {
			var name string
			switch {
			case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
				name = strings.TrimSpace(line[4:])
				if strings.HasPrefix(name, `"`) {
					decoded, err := strconv.Unquote(name)
					if err != nil {
						continue
					}
					name = decoded
				}
				if !strings.HasPrefix(name, "a/") && !strings.HasPrefix(name, "b/") {
					continue
				}
				name = name[2:]
			default:
				continue
			}
			if !mergeReportSafePath(name) {
				continue
			}
			if reason, ok := related[path.Dir(name)]; ok {
				matches[name] = reason
			}
		}
		names := make([]string, 0, len(matches))
		for name := range matches {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if len(candidates) < 20 {
				candidates = append(candidates, fmt.Sprintf("%s: %s (%s)", labels[c.Key], boundedTestName(name), boundedTestName(matches[name])))
			}
		}
	}
	return candidates
}

func mergeReportSafePath(name string) bool {
	return name != "" && !path.IsAbs(name) && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.Contains(name, `\`)
}
