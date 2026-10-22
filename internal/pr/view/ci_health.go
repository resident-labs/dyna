package view

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/cihealth"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/subagent"
)

// quietAfter is how long a test stays quiet before its next breach counts as
// a new crossing and may post again.
const quietAfter = 7 * 24 * time.Hour

// slowTestGap separates two breach episodes, as the watcher's crossing rule does.
const slowTestGap = time.Hour

// CIHealth is the room's CI graph and its slow-test list: what the tab shows
// before any drill-down.
type CIHealth struct {
	Room    string         `json:"room"`
	LimitMs int64          `json:"limitMs"`
	WarnMs  int64          `json:"warnMs"`
	FailMs  int64          `json:"failMs"`
	Runs    []CIHealthRun  `json:"runs"`
	Tests   []CIHealthTest `json:"tests"`
}

// CIHealthRun is one run of the graph: its times and the jobs that make them
// up, so a column can be drawn without opening the run.
type CIHealthRun struct {
	Token      string            `json:"token"`
	PR         int               `json:"pr"`
	Head       string            `json:"head"`
	Workflow   string            `json:"workflow"`
	Started    time.Time         `json:"started"`
	Status     string            `json:"status"`
	Conclusion string            `json:"conclusion"`
	QueueMs    int64             `json:"queueMs"`
	BusyMs     int64             `json:"busyMs"`
	WarnMs     int64             `json:"warnMs"`
	FailMs     int64             `json:"failMs"`
	Jobs       []CIHealthJobBand `json:"jobs,omitempty"`
}

// CIHealthJobBand is one job's band in a run's column: no steps, because the
// graph only needs the time and the order the jobs ran in.
type CIHealthJobBand struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	BusyMs int64  `json:"busyMs"`
}

// CIHealthTest is one slow-test row: its latest time, its trend and its state.
type CIHealthTest struct {
	Test      string            `json:"test"`
	Name      string            `json:"name"`
	Package   string            `json:"package"`
	Features  []string          `json:"features"`
	LimitMs   int64             `json:"limitMs"`
	LastMs    int64             `json:"lastMs"`
	TypicalMs int64             `json:"typicalMs"`
	Breaches  int               `json:"breaches"`
	State     string            `json:"state"`
	Ignored   string            `json:"ignored,omitempty"`
	Runs      []CIHealthTestRun `json:"runs"`
}

// CIHealthTestRun is one stored run of a test, for its trend and its breaches.
// It carries the test and package too, so a run's response names each test and
// a client can open the test without a second lookup.
type CIHealthTestRun struct {
	Test       string    `json:"test"`
	Package    string    `json:"package"`
	Token      string    `json:"token"`
	PR         int       `json:"pr"`
	Head       string    `json:"head"`
	At         time.Time `json:"at"`
	DurationMs int64     `json:"durationMs"`
	Over       bool      `json:"over"`
}

// CIHealthRunDetail is one run opened: its jobs and steps, and its slowest tests.
type CIHealthRunDetail struct {
	Run   CIHealthRun       `json:"run"`
	Jobs  []CIHealthJob     `json:"jobs"`
	Tests []CIHealthTestRun `json:"tests"`
}

// CIHealthJob is one job of an opened run. A job that waited for a runner has
// no steps and no busy time.
type CIHealthJob struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	Conclusion string         `json:"conclusion"`
	Started    time.Time      `json:"started,omitempty"`
	Completed  time.Time      `json:"completed,omitempty"`
	BusyMs     int64          `json:"busyMs"`
	Steps      []CIHealthStep `json:"steps"`
}

// CIHealthStep is one executed step of a job.
type CIHealthStep struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
}

// CIHealthTestDetail is one test opened: the same row with its whole history.
type CIHealthTestDetail struct {
	Test CIHealthTest `json:"test"`
}

// CIHealth reads the room's CI runs and slow tests. runs bounds the graph, and
// the tests with history are capped so one busy room cannot flood a client.
func (s *Service) CIHealth(roomRef string, runs int) (CIHealth, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return CIHealth{}, err
	}
	policy, err := s.Watcher().RoomChangePolicy(rec.ID)
	if err != nil {
		return CIHealth{}, err
	}
	limit, allowed, err := policy.TestBudget()
	if err != nil {
		return CIHealth{}, err
	}
	warn, fail, err := policy.CIBudget()
	if err != nil {
		return CIHealth{}, err
	}
	if runs <= 0 {
		runs = 30
	}
	ctx := context.Background()
	rows, err := s.ciHealthStore().Runs(ctx, rec.ID, runs)
	if err != nil {
		return CIHealth{}, err
	}
	history, err := s.ciHealthStore().Tests(ctx, rec.ID)
	if err != nil {
		return CIHealth{}, err
	}
	entries := s.libraryEntries(ctx, rec.Repo)
	now := time.Now()
	out := CIHealth{Room: rec.Name, LimitMs: limit.Milliseconds(), WarnMs: warn.Milliseconds(), FailMs: fail.Milliseconds(), Runs: make([]CIHealthRun, 0, len(rows))}
	tokens := make([]string, 0, len(rows))
	for _, run := range rows {
		tokens = append(tokens, run.Token)
	}
	jobs, err := s.ciHealthStore().Jobs(ctx, rec.ID, tokens)
	if err != nil {
		return CIHealth{}, err
	}
	for _, run := range rows {
		row := ciHealthRun(run)
		row.Jobs = jobBands(jobs[run.Token])
		out.Runs = append(out.Runs, row)
	}
	for _, test := range history {
		out.Tests = append(out.Tests, s.ciHealthTestRow(test, limit, allowed, policy.CITestAllowNotes, entries, now))
	}
	sortSlowTests(out.Tests)
	return out, nil
}

// CIHealthRun opens one run: its jobs, their steps, and the tests that ran slow.
func (s *Service) CIHealthRun(roomRef, token string) (CIHealthRunDetail, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return CIHealthRunDetail{}, err
	}
	detail, err := s.ciHealthStore().Run(context.Background(), rec.ID, token)
	if err != nil {
		return CIHealthRunDetail{}, err
	}
	return ciHealthRunDetail(detail), nil
}

// ciHealthRunDetail turns one stored run into the response: its jobs with
// their steps, and each slow test with the test and package it belongs to.
func ciHealthRunDetail(detail cihealth.RunDetail) CIHealthRunDetail {
	out := CIHealthRunDetail{Run: ciHealthRun(detail.Run), Jobs: make([]CIHealthJob, 0, len(detail.Jobs)), Tests: make([]CIHealthTestRun, 0, len(detail.Tests))}
	for _, job := range detail.Jobs {
		row := CIHealthJob{ID: job.ID, Name: job.Name, Status: job.Status, Conclusion: job.Conclusion,
			Started: job.Started, Completed: job.Completed, BusyMs: job.Busy.Milliseconds(), Steps: make([]CIHealthStep, 0, len(job.Steps))}
		for _, step := range job.Steps {
			row.Steps = append(row.Steps, CIHealthStep{Name: step.Name, Ms: step.Duration.Milliseconds()})
		}
		out.Jobs = append(out.Jobs, row)
	}
	for _, test := range detail.Tests {
		out.Tests = append(out.Tests, ciHealthTestRun(test))
	}
	return out
}

// CIHealthTest opens one test with its stored history.
func (s *Service) CIHealthTest(roomRef, test string) (CIHealthTestDetail, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return CIHealthTestDetail{}, err
	}
	policy, err := s.Watcher().RoomChangePolicy(rec.ID)
	if err != nil {
		return CIHealthTestDetail{}, err
	}
	limit, allowed, err := policy.TestBudget()
	if err != nil {
		return CIHealthTestDetail{}, err
	}
	ctx := context.Background()
	history, err := s.ciHealthStore().Tests(ctx, rec.ID)
	if err != nil {
		return CIHealthTestDetail{}, err
	}
	entries := s.libraryEntries(ctx, rec.Repo)
	for _, row := range history {
		if row.Test == test {
			return CIHealthTestDetail{Test: s.ciHealthTestRow(row, limit, allowed, policy.CITestAllowNotes, entries, time.Now())}, nil
		}
	}
	return CIHealthTestDetail{}, fmt.Errorf("no stored runs for %s", test)
}

func ciHealthRun(run cihealth.Run) CIHealthRun {
	return CIHealthRun{Token: run.Token, PR: run.PR, Head: run.Head, Workflow: run.Workflow, Started: run.Started,
		Status: run.Status, Conclusion: run.Conclusion, QueueMs: run.Queue.Milliseconds(), BusyMs: run.Busy.Milliseconds(),
		WarnMs: run.Warn.Milliseconds(), FailMs: run.Fail.Milliseconds()}
}

func ciHealthTestRun(run cihealth.Test) CIHealthTestRun {
	return CIHealthTestRun{Test: run.Test, Package: run.Package, Token: run.Token, PR: run.PR, Head: run.Head,
		At: run.Started, DurationMs: run.Duration.Milliseconds(), Over: run.Over()}
}

// jobBands keeps the jobs a column draws: the time each one took, in the
// order they started. A job that never ran adds no band.
func jobBands(jobs []cihealth.Job) []CIHealthJobBand {
	bands := make([]CIHealthJobBand, 0, len(jobs))
	for _, job := range jobs {
		if job.Busy == 0 {
			continue
		}
		bands = append(bands, CIHealthJobBand{ID: job.ID, Name: job.Name, BusyMs: job.Busy.Milliseconds()})
	}
	return bands
}

// ciHealthTestRow builds one row and the state that says how it should look.
func (s *Service) ciHealthTestRow(history cihealth.TestHistory, limit time.Duration, allowed map[string]bool, notes map[string]string, entries []library.Entry, now time.Time) CIHealthTest {
	row := CIHealthTest{
		Test: history.Test, Name: bareName(history.Test, history.Package), Package: history.Package,
		Features: library.PackageOwners(entries, history.Package),
		LimitMs:  limit.Milliseconds(), Runs: make([]CIHealthTestRun, 0, len(history.Runs)),
	}
	if len(history.Runs) > 0 {
		row.LimitMs = history.Runs[0].Limit.Milliseconds()
		row.LastMs = history.Runs[0].Duration.Milliseconds()
	}
	for _, run := range history.Runs {
		if run.Over() {
			row.Breaches++
		}
		row.Runs = append(row.Runs, ciHealthTestRun(run))
	}
	row.TypicalMs = medianMs(history.Runs)
	ignored, reason := ignoredTest(history.Test, history.Package, allowed, notes)
	if ignored {
		row.Ignored = reason
		if row.Ignored == "" {
			row.Ignored = "allowed in this room"
		}
	}
	row.State = testState(row, history.Runs, ignored, now)
	return row
}

// testState names what a person needs to know before acting: a test that is
// new, one that keeps breaking the limit, one that has settled, or one this
// room chose to allow.
func testState(row CIHealthTest, runs []cihealth.Test, ignored bool, now time.Time) string {
	if ignored {
		return "ignored"
	}
	var breaches []cihealth.Test
	for _, run := range runs {
		if run.Over() {
			breaches = append(breaches, run)
		}
	}
	if len(breaches) == 0 || now.Sub(breaches[0].Started) > quietAfter {
		return "quiet"
	}
	if len(breaches) < 2 {
		return "new"
	}
	for _, other := range breaches[1:] {
		if breaches[0].Started.Sub(other.Started) >= slowTestGap || other.Started.Sub(breaches[0].Started) >= slowTestGap {
			return "recurring"
		}
	}
	return "new"
}

func medianMs(runs []cihealth.Test) int64 {
	if len(runs) == 0 {
		return 0
	}
	times := make([]int64, 0, len(runs))
	for _, run := range runs {
		times = append(times, run.Duration.Milliseconds())
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2]
}

// sortSlowTests puts what needs work first: over the limit, then by how far
// over, then the newest.
func sortSlowTests(tests []CIHealthTest) {
	sort.SliceStable(tests, func(i, j int) bool {
		a, b := tests[i], tests[j]
		if (a.Breaches > 0) != (b.Breaches > 0) {
			return a.Breaches > 0
		}
		if a.Breaches != b.Breaches {
			return a.Breaches > b.Breaches
		}
		return a.LastMs > b.LastMs
	})
}

// bareName is the test name without its package prefix, which is the name a
// report and an allow list use: TestX, or TestX/subtest for a Go subtest.
func bareName(test, pkg string) string {
	if pkg == "" {
		return test
	}
	if name, ok := strings.CutPrefix(test, pkg+"/"); ok {
		return name
	}
	return test
}

// ciHealthStore keeps the rows beside the watcher's own state.
func (s *Service) ciHealthStore() cihealth.Store {
	return cihealth.Store{DataDir: s.dataDir()}
}

func (s *Service) libraryEntries(ctx context.Context, repoDir string) []library.Entry {
	if repoDir == "" {
		return nil
	}
	id, err := repoid.Resolve(ctx, repoDir)
	if err != nil {
		return nil
	}
	entries, err := library.Store{DataDir: s.dataDir()}.List(id.ID, false)
	if err != nil {
		return nil
	}
	return entries
}

// dataDir is the state folder that holds the PR records and the CI health rows.
func (s *Service) dataDir() string {
	root := s.Notify.Sessions.Store.Root
	if root == "" {
		root = subagent.DefaultStore().Root
	}
	return filepath.Dir(root)
}

// ignoredTest uses the same exact IDs as budget enforcement.
func ignoredTest(test, pkg string, allowed map[string]bool, notes map[string]string) (bool, string) {
	if allowed[test] {
		return true, notes[test]
	}
	return false, ""
}
