package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/textutil"
)

type Ref struct {
	Host, Repo string
	Number     int
}

func (r Ref) Key() string {
	return fmt.Sprintf("%s/%s#%d", strings.ToLower(r.Host), strings.ToLower(r.Repo), r.Number)
}
func (r Ref) URL() string { return fmt.Sprintf("https://%s/%s/pull/%d", r.Host, r.Repo, r.Number) }
func (r Ref) path(suffix string) string {
	return fmt.Sprintf("repos/%s/pulls/%d%s", r.Repo, r.Number, suffix)
}

type User struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}
type Comment struct {
	Association  string `json:"author_association"`
	Path         string `json:"path,omitempty"`
	Line         int    `json:"line,omitempty"`
	OriginalLine int    `json:"original_line,omitempty"`
	ID           int64  `json:"id"`
	Body         string `json:"body"`
	User         User   `json:"user"`
	URL          string `json:"html_url"`
	Created      string `json:"created_at"`
	Updated      string `json:"updated_at"`
	Submitted    string `json:"submitted_at"`
	ReviewID     int64  `json:"pull_request_review_id"`
	Commit       string `json:"commit_id"`
	State        string `json:"state"`
	Kind         string `json:"kind,omitempty"`
}
type Pull struct {
	Draft   bool   `json:"draft"`
	Updated string `json:"updated_at"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	// MergeCommit is the merge commit of a merged PR; it names the fix in issue closes.
	MergeCommit string `json:"merge_commit_sha"`
	User        User   `json:"user"`
	Head        struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo struct {
			DefaultBranch string `json:"default_branch"`
		} `json:"repo"`
	} `json:"base"`
}
type Check struct {
	CheckSuite struct {
		ID int64 `json:"id"`
	} `json:"check_suite"`
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"details_url"`
	Started    string `json:"started_at"`
	Completed  string `json:"completed_at"`
	App        struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"app"`
}
type Run struct {
	CheckSuiteID int64  `json:"check_suite_id"`
	HeadSHA      string `json:"head_sha"`
	HeadBranch   string `json:"head_branch"`
	Started      string `json:"run_started_at"`
	Event        string `json:"event"`
	ID           int64  `json:"id"`
	WorkflowID   int64  `json:"workflow_id"`
	Attempt      int    `json:"run_attempt"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
}
type Status struct {
	Description string `json:"description"`
	Creator     User   `json:"creator"`
	ID          int64  `json:"id"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Updated     string `json:"updated_at"`
	URL         string `json:"target_url"`
}
type Snapshot struct {
	OwnerLogin    string
	localCI       *registeredCIObservation
	ciReused      bool // A discussion refresh does not advance the full CI safety clock.
	headConfirmed bool
	ciProbe       string
	// NoPRWorkflows is true only after a successful workflow inspection.
	NoPRWorkflows bool
	Pull          Pull
	Checks        []Check
	Runs          []Run
	Statuses      []Status
	Comments      []Comment
}

// CheckSummary is the last observed per-check state for every client.
type CheckSummary = state.CheckSummary

func checkState(status, conclusion string) string {
	if status != "completed" || conclusion == "" {
		return "pending"
	}
	switch conclusion {
	case "cancelled", "skipped":
		return conclusion
	}
	if success(conclusion) {
		return "success"
	}
	return "failure"
}

// currentSuiteChecks retains each suite's latest jobs from the selected runs.
// Unknown suites and non-Actions checks retain their latest result per suite.
func (s Snapshot) currentSuiteChecks() []Check {
	suites := map[int64]bool{}
	for _, run := range s.Runs {
		if run.CheckSuiteID != 0 {
			suites[run.CheckSuiteID] = false
		}
	}
	for _, run := range currentRuns(s.Runs) {
		if run.CheckSuiteID != 0 {
			suites[run.CheckSuiteID] = true
		}
	}
	latest := map[string]Check{}
	for _, c := range s.Checks {
		if selected, known := suites[c.CheckSuite.ID]; c.App.Slug == "github-actions" && known && !selected {
			continue
		}
		key := fmt.Sprintf("%d/%d/%s/%s", c.CheckSuite.ID, c.App.ID, c.App.Slug, c.Name)
		if old, ok := latest[key]; !ok || c.ID > old.ID {
			latest[key] = c
		}
	}
	out := make([]Check, 0, len(latest))
	for _, c := range latest {
		out = append(out, c)
	}
	return out
}

// currentChecks keeps the latest result for each app and job name across suites.
func (s Snapshot) currentChecks() []Check {
	latest := map[string]Check{}
	for _, c := range s.currentSuiteChecks() {
		key := fmt.Sprintf("%d/%s/%s", c.App.ID, c.App.Slug, c.Name)
		if old, ok := latest[key]; !ok || c.ID > old.ID {
			latest[key] = c
		}
	}
	out := make([]Check, 0, len(latest))
	for _, c := range latest {
		out = append(out, c)
	}
	return out
}

func (s Snapshot) CheckSummaries(ref Ref) []CheckSummary {
	out := []CheckSummary{}
	for _, c := range s.currentChecks() {
		out = append(out, CheckSummary{ID: c.ID, Completed: c.Completed, Name: Sanitize(c.Name), State: checkState(c.Status, c.Conclusion), Status: c.Status, Conclusion: c.Conclusion, URL: c.URL, CommentURL: s.checkCommentURL(c.URL, c.Name, c.App.Slug, c.App.Name)})
	}
	statuses := map[string]Status{}
	for _, v := range s.Statuses {
		key := v.Context
		if localCommitStatus(v) {
			key = "local:" + key
		} else {
			key = "hosted:" + key
		}
		if old, ok := statuses[key]; !ok || v.ID > old.ID {
			statuses[key] = v
		}
	}
	for _, v := range statuses {
		if s.localCI != nil && localCommitStatus(v) {
			continue
		}
		status := "completed"
		if v.State == "pending" || v.State == "" {
			status = "in_progress"
		}
		out = append(out, CheckSummary{Node: localStatusNode(v), Name: Sanitize(v.Context), State: checkState(status, v.State), Status: status, Conclusion: v.State, URL: v.URL, CommentURL: s.checkCommentURL(v.URL, v.Context)})
	}
	if s.localCI != nil {
		out = append(out, s.localCI.Checks...)
	}
	if len(out) == 0 {
		for _, r := range currentRuns(s.Runs) {
			out = append(out, CheckSummary{Name: Sanitize(r.Name), State: checkState(r.Status, r.Conclusion), Status: r.Status, Conclusion: r.Conclusion, URL: fmt.Sprintf("https://%s/%s/actions/runs/%d", ref.Host, ref.Repo, r.ID)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].URL < out[j].URL
	})
	return out
}

// checkBotKey matches app identities, not arbitrary substrings in check names.
func checkBotKey(name string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), "[bot]")
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, name)
	switch name {
	case "coderabbit", "coderabbitai":
		return "coderabbitai"
	case "copilot", "copilotpullrequestreviewer":
		return "copilot"
	case "greptile", "greptileapps":
		return "greptile"
	case "codex", "chatgptcodexconnector":
		return "codex"
	}
	return name
}

func commentTime(c Comment) time.Time {
	var latest time.Time
	for _, value := range []string{c.Created, c.Updated, c.Submitted} {
		if at, err := time.Parse(time.RFC3339, value); err == nil && at.After(latest) {
			latest = at
		}
	}
	return latest
}

// A bot check sometimes has no details URL. Its latest posted comment or
// review gives clients a useful destination, including for a noise-only check.
func (s Snapshot) checkCommentURL(detailsURL string, identities ...string) string {
	if detailsURL != "" {
		return ""
	}
	keys := map[string]bool{}
	for _, name := range identities {
		if key := checkBotKey(name); key != "" {
			keys[key] = true
		}
	}
	var latest Comment
	var found bool
	for _, c := range s.Comments {
		if !isBot(c.User) || c.URL == "" || strings.EqualFold(c.State, "PENDING") || !keys[checkBotKey(c.User.Login)] {
			continue
		}
		at, previous := commentTime(c), commentTime(latest)
		if !found || at.After(previous) || (at.Equal(previous) && c.ID > latest.ID) {
			latest, found = c, true
		}
	}
	return latest.URL
}

type GitHub struct {
	RoomID         string
	Rooms          *room.Store
	Executable     string
	Timeout        time.Duration
	recoverPolling func(RateLimit, RateLimitScope) error
	artifactErrors bool
	now            func() time.Time
	requests       *requestCache
	// run replaces process execution in tests; production uses the shared account-aware path.
	run func(context.Context, *exec.Cmd) error
}

func (g GitHub) command(ctx context.Context, dir string, tail bool, args ...string) ([]byte, error) {
	return g.commandLimit(ctx, dir, tail, 4*1024*1024, args...)
}

// ghSlots bounds the gh processes of all watchers. On a busy machine many
// parallel processes starve each other past the request deadline. Poll
// workers can never take every slot.
var ghSlots = make(chan struct{}, snapshotWorkers+2)

var errGHResponseLimit = errors.New("gh response exceeds size limit")
var headSHAPattern = regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`)

func (g GitHub) requestTimeout() time.Duration {
	if g.Timeout != 0 {
		return g.Timeout
	}
	return 20 * time.Second
}

func (g GitHub) commandLimit(ctx context.Context, dir string, tail bool, limit int, args ...string) ([]byte, error) {
	return g.commandWithAccount(ctx, dir, tail, limit, nil, args...)
}

func (g GitHub) commandWithAccount(ctx context.Context, dir string, tail bool, limit int, before func(string) error, args ...string) ([]byte, error) {
	if g.Rooms != nil {
		ctx = ghclient.WithRooms(ctx, g.Rooms)
	}
	if g.RoomID != "" {
		ctx = ghclient.WithRoom(ctx, g.RoomID)
	}
	// Wait for a slot before the request deadline starts, so queued requests
	// are never killed for time they spent waiting.
	select {
	case ghSlots <- struct{}{}:
		defer func() { <-ghSlots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("gh request failed: %w", ctx.Err())
	}
	ctx, cancel := context.WithTimeout(ctx, g.requestTimeout())
	defer cancel()
	cmd := ghclient.Command(ctx, g.Executable, dir, args...)
	out := boundedio.New(limit)
	if tail {
		out = boundedio.NewTail(8000)
	}
	cmd.Stdout = out
	// Never echo gh stderr: authentication failures can include credentials.
	run := func() error { return ghclient.RunWithAccount(ctx, cmd, before) }
	if g.run != nil {
		run = func() error {
			if before != nil {
				if err := before(""); err != nil {
					return err
				}
			}
			return g.run(ctx, cmd)
		}
	}
	err := run()
	if g.artifactErrors {
		err = artifactCommandError(ctx, out.Bytes(), err)
	}
	if err != nil {
		return append([]byte(nil), out.Bytes()...), fmt.Errorf("gh request failed: %w", errors.Join(err, ctx.Err()))
	}
	if out.Truncated() && !tail {
		return nil, fmt.Errorf("%w (%d bytes)", errGHResponseLimit, limit)
	}
	return append([]byte(nil), out.Bytes()...), nil
}

// Tests count actual saved-CI decoding while keeping the production decoder.
var unmarshalSavedCI = json.Unmarshal

func (g GitHub) decodeResponse(body []byte, out any) error {
	if g.requests != nil && g.requests.cacheOnly {
		return unmarshalSavedCI(body, out)
	}
	return json.Unmarshal(body, out)
}

func (g GitHub) api(ctx context.Context, r Ref, path string, out any) error {
	body, _, err := g.request(ctx, r, path)
	if err != nil {
		return err
	}
	return g.decodeResponse(body, out)
}
func (g GitHub) pages(ctx context.Context, r Ref, path string, out any) error {
	// Fetch each page separately: gh --paginate would send one validator to
	// every page, although each page has its own ETag and cached Link header.
	var pages []json.RawMessage
	seen := map[string]bool{}
	size := 0
	for path != "" {
		if seen[path] {
			return fmt.Errorf("GitHub pagination cycle")
		}
		seen[path] = true
		body, next, err := g.request(ctx, r, path)
		if err != nil {
			return err
		}
		size += len(body)
		if size > 4*1024*1024 {
			return fmt.Errorf("gh response exceeds 4 MiB")
		}
		pages = append(pages, json.RawMessage(body))
		path = next
	}
	body, err := json.Marshal(pages)
	if err != nil {
		return err
	}
	return g.decodeResponse(body, out)
}
func (g GitHub) Resolve(ctx context.Context, text, dir string) (Ref, error) {
	if strings.Contains(text, "://") {
		u, err := url.Parse(text)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return Ref{}, fmt.Errorf("invalid PR URL")
		}
		p := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(p) != 4 || p[2] != "pull" {
			return Ref{}, fmt.Errorf("expected a pull request URL")
		}
		n, err := strconv.Atoi(p[3])
		if err != nil || n < 1 || u.Hostname() == "" {
			return Ref{}, fmt.Errorf("invalid PR number")
		}
		return Ref{u.Host, p[0] + "/" + p[1], n}, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 {
		return Ref{}, fmt.Errorf("use a PR number or URL")
	}
	b, err := g.command(ctx, dir, false, "repo", "view", "--json", "nameWithOwner,url")
	if err != nil {
		return Ref{}, err
	}
	var repo struct {
		Name string `json:"nameWithOwner"`
		URL  string `json:"url"`
	}
	if err = json.Unmarshal(b, &repo); err != nil {
		return Ref{}, err
	}
	u, err := url.Parse(repo.URL)
	if err != nil || u.Host == "" || len(strings.Split(repo.Name, "/")) != 2 {
		return Ref{}, fmt.Errorf("gh returned an invalid repository")
	}
	return Ref{u.Host, repo.Name, n}, nil
}

// Pull fetches metadata without the CI and discussion requests of Snapshot.
// It uses the same request cache and rate-limit handling as watched polls.
func (g GitHub) Pull(ctx context.Context, r Ref) (Pull, error) {
	var pull Pull
	err := g.api(ctx, r, r.path(""), &pull)
	return pull, err
}

// pullFile is the facts a changed-path read uses. A rename keeps both names:
// a production file moved into docs/ is still a production change.
type pullFile struct {
	Name     string `json:"filename"`
	Previous string `json:"previous_filename,omitempty"`
}

// ChangedPaths reads the paths a pull request touches, from GitHub and not from
// the reviewer. A PR with more files than one page is not low risk, because the
// fast-landing rule needs every path. Both sides of a rename count.
func (g GitHub) ChangedPaths(ctx context.Context, r Ref) ([]string, error) {
	var files []pullFile
	if err := g.api(ctx, r, r.path("/files")+"?per_page=100", &files); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(files)*2)
	for _, f := range files {
		paths = append(paths, f.Name)
		if f.Previous != "" && f.Previous != f.Name {
			paths = append(paths, f.Previous)
		}
	}
	if len(files) == 100 {
		return nil, fmt.Errorf("the change has more than 100 files")
	}
	return paths, nil
}

func (g GitHub) Snapshot(ctx context.Context, r Ref) (Snapshot, error) {
	var s Snapshot
	if err := g.api(ctx, r, r.path(""), &s.Pull); err != nil {
		return s, err
	}
	return g.snapshotFromPull(ctx, r, s.Pull, nil)
}

// snapshotFromPull reuses metadata fetched by the cheap poll probe.
func (g GitHub) snapshotFromPull(ctx context.Context, r Ref, pull Pull, savedCI *Snapshot) (Snapshot, error) {
	if g.requests == nil {
		g.requests = &requestCache{now: g.now}
	}
	s := Snapshot{Pull: pull, OwnerLogin: g.ownerLogin()}
	if s.Pull.State == "closed" {
		return s, nil
	}
	sha := s.Pull.Head.SHA
	if !headSHAPattern.MatchString(sha) {
		return s, fmt.Errorf("gh returned an invalid head SHA")
	}
	if savedCI == nil {
		if err := g.readCommitCI(ctx, r, sha, &s); err != nil {
			return s, err
		}
	} else {
		s.Checks, s.Runs, s.Statuses = savedCI.Checks, savedCI.Runs, savedCI.Statuses
		s.ciReused = true
	}
	var err error
	s.Comments, err = g.Comments(ctx, r)
	if err == nil && !s.hasRealCI() {
		active, workflowErr := g.hasPRWorkflows(ctx, r)
		s.NoPRWorkflows = workflowErr == nil && !active
		// Missing workflow permissions leave CI unknown. The grace window still applies.
		var limited *rateLimitError
		if errors.As(workflowErr, &limited) {
			return s, workflowErr
		}
	}
	s.OwnerLogin = g.ownerLogin()
	return s, err
}

// readCommitCI is shared by PR polling and exact merge-trial commits.
func (g GitHub) readCommitCI(ctx context.Context, r Ref, sha string, s *Snapshot) error {
	if !headSHAPattern.MatchString(sha) {
		return fmt.Errorf("invalid CI commit")
	}
	if err := g.readCommitChecks(ctx, r, sha, s); err != nil {
		return err
	}
	runs, err := g.readCommitRuns(ctx, r, sha)
	if err != nil {
		return err
	}
	s.Runs = append(s.Runs, runs...)
	return g.readCommitStatuses(ctx, r, sha, s)
}

func (g GitHub) readCommitChecks(ctx context.Context, r Ref, sha string, s *Snapshot) error {
	var pages []struct {
		Checks []Check `json:"check_runs"`
	}
	if err := g.pages(ctx, r, "repos/"+r.Repo+"/commits/"+sha+"/check-runs?per_page=100&filter=latest", &pages); err != nil {
		return err
	}
	for _, page := range pages {
		s.Checks = append(s.Checks, page.Checks...)
	}
	return nil
}

func (g GitHub) readCommitStatuses(ctx context.Context, r Ref, sha string, s *Snapshot) error {
	var pages [][]Status
	if err := g.pages(ctx, r, "repos/"+r.Repo+"/commits/"+sha+"/statuses?per_page=100", &pages); err != nil {
		return err
	}
	s.Statuses = nil
	for _, page := range pages {
		s.Statuses = append(s.Statuses, page...)
	}
	return nil
}

// readCommitRuns shares pagination and run decoding with the full CI read.
func (g GitHub) readCommitRuns(ctx context.Context, r Ref, sha string) ([]Run, error) {
	if !headSHAPattern.MatchString(sha) {
		return nil, fmt.Errorf("invalid CI commit")
	}
	var pages []struct {
		Runs []Run `json:"workflow_runs"`
	}
	if err := g.pages(ctx, r, "repos/"+r.Repo+"/actions/runs?head_sha="+sha+"&per_page=100", &pages); err != nil {
		return nil, err
	}
	var runs []Run
	for _, page := range pages {
		runs = append(runs, page.Runs...)
	}
	return runs, nil
}

// Comments is also used when learning from an ended PR; normal watch polls
// can still stop early once the PR closes.
func (g GitHub) Comments(ctx context.Context, r Ref) ([]Comment, error) {
	comments := []Comment{}
	for _, kind := range []string{"reviews", "comments", "issue-comments"} {
		path := r.path("/" + kind)
		if kind == "issue-comments" {
			path = fmt.Sprintf("repos/%s/issues/%d/comments", r.Repo, r.Number)
		}
		var pages [][]Comment
		if err := g.pages(ctx, r, path+"?per_page=100", &pages); err != nil {
			return nil, err
		}
		for _, page := range pages {
			for _, comment := range page {
				if kind == "reviews" && comment.State == "PENDING" {
					continue
				}
				comment.Kind = kind
				comments = append(comments, comment)
			}
		}
	}
	return comments, nil
}
func refRecord(r Record) Ref { return Ref{r.Host, r.Repo, r.Number} }
func (g GitHub) Logs(ctx context.Context, r Ref, run Run) string {
	args := []string{"run", "view", strconv.FormatInt(run.ID, 10), "--repo", r.Host + "/" + r.Repo, "--log-failed"}
	if run.Attempt > 0 {
		args = append(args, "--attempt", strconv.Itoa(run.Attempt))
	}
	b, err := g.command(ctx, "", true, args...)
	if err != nil {
		return "Log tail unavailable; inspect the failed job on GitHub."
	}
	return Sanitize(string(b))
}

// A required name can identify an external check or a local check scope.
// Only names present in this GitHub snapshot affect the GitHub CI gate.
func requiredCheck(name string, required []string) bool {
	for _, entry := range required {
		if name == entry {
			return true
		}
	}
	return false
}

func ciCheck(c Check, required []string) bool {
	return c.App.Slug == "github-actions" || requiredCheck(c.Name, required)
}

func (s Snapshot) CIStatus(required ...string) (state, key string, jobs []string, failedRuns []Run) {
	if s.localCI != nil {
		return string(s.localCI.Status), s.localCI.Key, s.localCI.Jobs, s.localCI.Runs
	}
	pending, failed, count := false, false, 0
	passed := false
	executedChecksBySuite := map[int64]bool{}
	var parts []string
	for _, c := range s.currentSuiteChecks() {
		if c.App.Slug == "github-actions" {
			executedChecksBySuite[c.CheckSuite.ID] = executedChecksBySuite[c.CheckSuite.ID] || c.Conclusion != "skipped"
			parts = append(parts, fmt.Sprintf("suite:%d:%d:%s:%s", c.CheckSuite.ID, c.ID, c.Status, c.Conclusion))
		}
	}
	for _, c := range s.currentChecks() {
		if !ciCheck(c, required) {
			continue
		}
		count++
		parts = append(parts, fmt.Sprintf("check:%d:%s:%s", c.ID, c.Status, c.Conclusion))
		// A cancelled check finished without testing the head. CI cannot pass
		// until a push or rerun, but the check names no failed job.
		if c.Status != "completed" || c.Conclusion == "" {
			pending = true
		} else if c.Conclusion == "cancelled" {
			failed = true
		} else if !success(c.Conclusion) {
			failed = true
			jobs = append(jobs, c.Name)
		} else if c.Conclusion != "skipped" {
			passed = true
		}
	}
	for _, r := range currentRuns(s.Runs) {
		count++
		parts = append(parts, fmt.Sprintf("run:%d:%d:%s:%s", r.ID, r.Attempt, r.Status, r.Conclusion))
		// Over-budget cancels fail through budgetFailure; other cancels fail
		// without a failed run.
		if r.Status != "completed" || r.Conclusion == "" {
			pending = true
		} else if r.Conclusion == "cancelled" {
			failed = true
		} else if !success(r.Conclusion) {
			failed = true
			failedRuns = append(failedRuns, r)
		} else if r.Conclusion != "skipped" {
			// An all-skipped suite supplies no passing work, even if its run reports success.
			if executed, observed := executedChecksBySuite[r.CheckSuiteID]; !observed || executed {
				passed = true
			}
		}
	}
	statuses := map[string]Status{}
	for _, v := range s.Statuses {
		if localCommitStatus(v) {
			continue
		}
		if old, ok := statuses[v.Context]; !ok || v.ID > old.ID {
			statuses[v.Context] = v
		}
	}
	for _, v := range statuses {
		if localCommitStatus(v) || !requiredCheck(v.Context, required) {
			continue
		}
		count++
		parts = append(parts, fmt.Sprintf("status:%d:%s", v.ID, v.State))
		switch v.State {
		case "pending", "":
			pending = true
		case "success":
			passed = true
		default:
			failed = true
			jobs = append(jobs, v.Context)
		}
	}
	sort.Strings(parts)
	sort.Strings(jobs)
	sort.Slice(failedRuns, func(i, j int) bool { return failedRuns[i].ID < failedRuns[j].ID })
	state = "passed"
	if failed {
		state = "failed"
	}
	if pending || count == 0 || (!failed && !passed) {
		state = "pending"
	}
	if count == 0 && s.NoPRWorkflows {
		state = "absent"
	}
	return state, digest(s.Pull.Head.SHA + strings.Join(parts, ";")), jobs, failedRuns
}

// ciOnlyCancelled reports CI that failed only because runs or checks were
// cancelled. Such CI did not test the head, so it must not count as a failure.
func (s Snapshot) ciOnlyCancelled(required ...string) bool {
	state, _, jobs, runs := s.CIStatus(required...)
	return state == "failed" && len(jobs) == 0 && len(runs) == 0 && s.ciCancelled(required...)
}

// ciCancelled reports a CI run or check whose latest result was cancelled.
func (s Snapshot) ciCancelled(required ...string) bool {
	for _, r := range currentRuns(s.Runs) {
		if r.Status == "completed" && r.Conclusion == "cancelled" {
			return true
		}
	}
	for _, c := range s.currentChecks() {
		if ciCheck(c, required) && c.Status == "completed" && c.Conclusion == "cancelled" {
			return true
		}
	}
	return false
}

// hasRealCI includes only Actions runs and explicitly required check names.
func (s Snapshot) hasRealCI(required ...string) bool {
	if s.localCI != nil {
		return true
	}
	if len(s.Runs) > 0 {
		return true
	}
	for _, c := range s.Checks {
		if ciCheck(c, required) {
			return true
		}
	}
	for _, status := range s.Statuses {
		if !localCommitStatus(status) && requiredCheck(status.Context, required) {
			return true
		}
	}
	return false
}

type workflowCapability struct {
	active    bool
	expires   time.Time
	responses map[string]cachedResponse
}

var workflowCapabilities = struct {
	sync.Mutex
	repos map[string]workflowCapability
}{repos: map[string]workflowCapability{}}

// Cache per host and repository. Keep validators across expiry and share the
// poller's rate-limit state when it supplies one.
func (g GitHub) hasPRWorkflows(ctx context.Context, r Ref) (bool, error) {
	now := time.Now()
	if g.now != nil {
		now = g.now()
	}
	if g.requests != nil {
		now = g.requests.clock()
	}
	key := g.Executable + ":" + strings.ToLower(r.Host+"/"+r.Repo)
	workflowCapabilities.Lock()
	cached := workflowCapabilities.repos[key]
	workflowCapabilities.Unlock()
	if now.Before(cached.expires) {
		return cached.active, nil
	}
	original := g.requests
	cache := requestCache{rates: map[string]RateLimit{}, now: func() time.Time { return now }}
	if original != nil {
		cache = *original
	}
	cache.entries = cached.responses
	cache.used = map[string]cachedResponse{}
	g.requests = &cache
	active, err := g.readPRWorkflows(ctx, r)
	if original != nil {
		for path, response := range g.requests.used {
			original.used[path] = response
		}
	}
	if err != nil {
		return false, err
	}
	workflowCapabilities.Lock()
	// Remove old repositories so a long-running host keeps a bounded cache.
	for repo, entry := range workflowCapabilities.repos {
		if now.After(entry.expires.Add(time.Hour)) {
			delete(workflowCapabilities.repos, repo)
		}
	}
	workflowCapabilities.repos[key] = workflowCapability{active: active, expires: now.Add(5 * time.Minute), responses: g.requests.used}
	workflowCapabilities.Unlock()
	return active, nil
}

func (g GitHub) readPRWorkflows(ctx context.Context, r Ref) (bool, error) {
	var pages []struct {
		Workflows []struct {
			Path  string `json:"path"`
			State string `json:"state"`
		} `json:"workflows"`
	}
	if err := g.pages(ctx, r, "repos/"+r.Repo+"/actions/workflows?per_page=100", &pages); err != nil {
		return false, err
	}
	for _, page := range pages {
		for _, workflow := range page.Workflows {
			if workflow.State != "active" {
				continue
			}
			var file struct {
				Content  string `json:"content"`
				Encoding string `json:"encoding"`
			}
			if err := g.api(ctx, r, "repos/"+r.Repo+"/contents/"+workflow.Path, &file); err != nil {
				return false, err
			}
			if file.Encoding != "base64" {
				return false, fmt.Errorf("unsupported workflow content encoding")
			}
			body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
			if err != nil {
				return false, err
			}
			enabled, err := workflowPREvent(body)
			if err != nil {
				return false, err
			}
			if enabled {
				return true, nil
			}
		}
	}
	return false, nil
}

func workflowPREvent(body []byte) (bool, error) {
	events, err := workflowEvents(body)
	if err != nil {
		return false, err
	}
	for event := range events {
		if event == "pull_request" || event == "pull_request_target" {
			return true, nil
		}
	}
	return false, nil
}

func success(s string) bool { return s == "success" || s == "neutral" || s == "skipped" }

// clipNote marks text that this package's clip cut. Its readers find the
// remaining text on GitHub.
const clipNote = "\n[truncated; read the full text on GitHub]"

// clip bounds s to n bytes, cut back to a whole rune, and names where the
// rest lives.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return textutil.Prefix(s, n) + clipNote
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

// keepLines trims each line and caps blank runs at one empty line, so the
// markdown in a review summary still renders as headings and lists.
func keepLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var secretEnvPattern = regexp.MustCompile(`(?i)TOKEN|SECRET|PASSWORD|API_KEY|ACCESS_KEY`)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|sk-[A-Za-z0-9_-]{12,}|AKIA[A-Z0-9]{16})\b`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)(?:bearer\s+|basic\s+)?[^\s]+`),
	regexp.MustCompile(`(?im)([A-Z0-9_]*(?:TOKEN|PASSWORD|SECRET|API_KEY|ACCESS_KEY)[A-Z0-9_]*\s*[=:]\s*)[^\r\n]+`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`https?://[^\s/@]+:[^\s/@]+@`),
}

func Sanitize(s string) string {
	for _, entry := range os.Environ() {
		k, v, ok := strings.Cut(entry, "=")
		if ok && len(v) >= 8 && secretEnvPattern.MatchString(k) {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	for _, p := range secretPatterns {
		s = p.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// publicationBody keeps published text sanitized.
func publicationBody(body string) string {
	return Sanitize(strings.TrimSpace(body))
}

func (s *Service) Comment(ctx context.Context, r Ref, session, body string, publish bool) error {
	if !publish {
		record, err := s.Store.Get(r.Key())
		if err != nil {
			return fmt.Errorf("GitHub publishing needs --publish for an untracked PR: %w", err)
		}
		policy, err := s.Publishing(record.RoomID)
		if err != nil {
			return err
		}
		if policy != PublishingGitHub {
			return fmt.Errorf("this room keeps feedback in Dyna; use a room post or dyna git pr verdict --head for an assigned review, or use --publish to explicitly post this comment to GitHub")
		}
	}
	// An empty session leaves the post and its replies as owner feedback.
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("comment body is required")
	}
	if len(body) > 30000 {
		return fmt.Errorf("comment exceeds 30000 bytes")
	}
	text := publicationBody(body)
	// Keep the per-PR routing gate across the bounded request, then take the
	// state lock only to claim its ID. Unrelated PR mutations remain available.
	ctx, cancel := context.WithTimeout(ctx, s.GH.requestTimeout())
	defer cancel()
	unlock, err := s.publicationGate(ctx, r.Key())
	if err != nil {
		return err
	}
	defer unlock()
	out, err := s.githubFor(r).command(ctx, "", false, "api", "--hostname", r.Host, fmt.Sprintf("repos/%s/issues/%d/comments", r.Repo, r.Number), "--method", "POST", "--raw-field", "body="+text)
	if err != nil {
		return err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		return fmt.Errorf("the comment is posted, but GitHub returned no comment ID: Dyna could not record its routing; do not post it again")
	}
	if created.ID < 1 {
		return fmt.Errorf("the comment is posted, but GitHub returned no comment ID; do not post it again")
	}
	posted := created.ID
	err = s.Store.update(func(st *State) error {
		claimComment(st, "issue-comments", posted, session)
		return nil
	})
	if err != nil {
		return err
	}
	log.Printf("PR %s: recorded comment %d for session %s", r.Key(), posted, session)
	return nil
}

// ReviewComment is one inline comment inside a published review.
type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Body string `json:"body"`
}

// PostReview publishes one GitHub comment review with its inline comments in a
// single request and claims the returned review ID for the session, so the
// review and its comments route before any watcher tick processes them.
func (s *Service) PostReview(ctx context.Context, r Ref, session, body, head string, comments []ReviewComment, publish bool) (int64, error) {
	if !publish {
		record, err := s.Store.Get(r.Key())
		if err != nil {
			return 0, fmt.Errorf("GitHub publishing needs --publish for an untracked PR: %w", err)
		}
		policy, err := s.Publishing(record.RoomID)
		if err != nil {
			return 0, err
		}
		if policy != PublishingGitHub {
			return 0, fmt.Errorf("this room keeps feedback in Dyna; use dyna git pr verdict --head for an assigned review, or use --publish to explicitly publish this review to GitHub")
		}
	}
	// An empty session leaves the review as owner feedback (see Comment).
	if strings.TrimSpace(body) == "" {
		return 0, fmt.Errorf("review body is required")
	}
	if len(body) > 30000 {
		return 0, fmt.Errorf("review exceeds 30000 bytes")
	}
	if head == "" {
		if record, err := s.Store.Get(r.Key()); err == nil {
			head = record.Head
		}
	}
	if head == "" {
		return 0, fmt.Errorf("review needs --head to name the reviewed commit")
	}
	if len(comments) > 100 {
		return 0, fmt.Errorf("review has more than 100 inline comments")
	}
	type inline struct {
		Path string `json:"path"`
		Line int    `json:"line,omitempty"`
		Body string `json:"body"`
	}
	payload := struct {
		CommitID string   `json:"commit_id"`
		Body     string   `json:"body"`
		Event    string   `json:"event"`
		Comments []inline `json:"comments,omitempty"`
	}{CommitID: head, Body: publicationBody(body), Event: "COMMENT"}
	for _, c := range comments {
		if strings.TrimSpace(c.Path) == "" || strings.TrimSpace(c.Body) == "" || c.Line < 0 {
			return 0, fmt.Errorf("each inline comment needs a path and a body")
		}
		if len(c.Body) > 30000 {
			return 0, fmt.Errorf("inline comment exceeds 30000 bytes")
		}
		payload.Comments = append(payload.Comments, inline{Path: c.Path, Line: c.Line, Body: publicationBody(c.Body)})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp("", "dyna-review-*.json")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return 0, err
	}
	if err = f.Close(); err != nil {
		return 0, err
	}
	// Lock order matches Comment and process: per-PR gate, then state.lock.
	ctx, cancel := context.WithTimeout(ctx, s.GH.requestTimeout())
	defer cancel()
	unlock, err := s.publicationGate(ctx, r.Key())
	if err != nil {
		return 0, err
	}
	defer unlock()
	out, err := s.githubFor(r).command(ctx, "", false, "api", "--hostname", r.Host, fmt.Sprintf("repos/%s/pulls/%d/reviews", r.Repo, r.Number), "--method", "POST", "--input", f.Name())
	if err != nil {
		return 0, err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(out, &created); err != nil {
		return 0, fmt.Errorf("the review is posted, but GitHub returned no review ID: Dyna could not record its routing; do not post it again")
	}
	if created.ID < 1 {
		return 0, fmt.Errorf("the review is posted, but GitHub returned no review ID; do not post it again")
	}
	reviewID := created.ID
	err = s.Store.update(func(st *State) error {
		claimComment(st, "reviews", reviewID, session)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return reviewID, nil
}

// openPulls includes untracked PRs that can still depend on a branch.
func (g GitHub) openPulls(ctx context.Context, ref Ref) ([]Pull, error) {
	var pages [][]Pull
	if err := g.pages(ctx, ref, fmt.Sprintf("repos/%s/pulls?state=open&per_page=100", ref.Repo), &pages); err != nil {
		return nil, err
	}
	var pulls []Pull
	for _, page := range pages {
		pulls = append(pulls, page...)
	}
	return pulls, nil
}

// ownerLogin uses the account that authenticated the snapshot requests. It
// must not repeat repository discovery while processing a completed snapshot.
func (g GitHub) ownerLogin() string {
	if g.requests == nil {
		return ""
	}
	return g.requests.account
}
