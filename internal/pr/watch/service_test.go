package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
)

type fixture struct {
	Discovery    []map[string]any
	Draft        bool
	Mergeability string
	CancelError  bool
	OpenPulls    []Pull
	Raw          map[string]string
	Headers      map[string]string
	Pull         Pull
	Checks       []Check
	Runs         []Run
	Statuses     []Status
	Comments     []Comment
	Log          string
	Fail         string
	Blobs        map[string][]byte
}

func TestFakeGH(t *testing.T) {
	if os.Getenv("PR_FAKE_GH") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(runFakeGH(args, os.Stdout, os.Stderr, os.Getenv))
}

// runFakeGH keeps fixture responses identical for process and in-process tests.
func runFakeGH(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	var f fixture
	b, err := os.ReadFile(getenv("PR_FIXTURE"))
	if err != nil {
		return 3
	}
	if json.Unmarshal(b, &f) != nil {
		return 4
	}
	log, _ := os.OpenFile(getenv("PR_CALLS"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if log != nil {
		fmt.Fprintln(log, strings.Join(args, " "))
		log.Close()
	}
	path := ""
	for _, a := range args {
		if a == "graphql" {
			path = a
		}
		if strings.HasPrefix(a, "repos/") {
			path = a
		}
	}
	if f.CancelError && strings.HasSuffix(path, "/cancel") {
		fmt.Fprint(stdout, "HTTP/2.0 403 Forbidden\r\n\r\n{\"message\":\"cancel denied\"}")
		return 1
	}
	if blob, ok := f.Blobs[path]; ok {
		for _, arg := range args {
			if arg == "--include" {
				fakeGHHeaders(stdout, "200 OK", "", f.Headers)
				break
			}
		}
		stdout.Write(blob)
		return 0
	}
	if raw, ok := f.Raw[path]; ok {
		fmt.Fprint(stdout, raw)
		if strings.HasPrefix(raw, "HTTP/2.0 403") || strings.HasPrefix(raw, "HTTP/2.0 429") {
			return 1
		}
		return 0
	}
	if f.Fail != "" && strings.Contains(path, f.Fail) {
		fmt.Fprintln(stderr, "Authorization: Bearer ghp_this_must_never_leave_stderr")
		return 2
	}
	var out any
	switch {
	case path == "graphql":
		out = fakeHeadBatch(f, args)
	case len(args) > 0 && args[0] == "repo":
		out = map[string]string{"nameWithOwner": "owner/repo", "url": "https://github.com/owner/repo"}
	case len(args) > 0 && args[0] == "run":
		if f.CancelError && len(args) > 1 && args[1] == "cancel" {
			fmt.Fprintln(stderr, "cancel denied")
			return 1
		}
		fmt.Fprint(stdout, f.Log)
		return 0
	case strings.Contains(path, "/pulls?"):
		out = f.OpenPulls
	case strings.Contains(path, "/artifacts?"):
		out = map[string]any{"artifacts": []any{}}
	case strings.Contains(path, "/actions/workflows?"):
		out = []any{map[string]any{"workflows": []any{map[string]string{"state": "active", "path": ".github/workflows/ci.yml"}}}}
	case strings.Contains(path, "/contents/.github/workflows/"):
		out = map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("on: pull_request\n"))}
	case strings.Contains(path, "/check-runs?"):
		out = []any{map[string]any{"check_runs": f.Checks}}
	case strings.Contains(path, "/actions/runs?"):
		out = []any{map[string]any{"workflow_runs": f.Runs}}
	case strings.Contains(path, "/statuses?"):
		out = f.Statuses
	case strings.Contains(path, "/reviews?") || strings.Contains(path, "/comments?"):
		kind := "comments"
		if strings.Contains(path, "/reviews?") {
			kind = "reviews"
		}
		if strings.Contains(path, "/issues/") {
			kind = "issue-comments"
		}
		comments := []Comment{}
		for _, c := range f.Comments {
			if c.Kind == kind {
				comments = append(comments, c)
			}
		}
		out = [][]Comment{comments}
	case strings.Contains(strings.Join(args, " "), "--method POST"):
		var posted []byte
		for i, a := range args {
			switch {
			case strings.HasPrefix(a, "body="):
				posted = []byte(strings.TrimPrefix(a, "body="))
			case a == "--input" && i+1 < len(args):
				posted, _ = os.ReadFile(args[i+1])
			}
		}
		if posted != nil {
			os.WriteFile(getenv("PR_POSTED"), posted, 0600)
		}
		out = map[string]any{"id": 123, "html_url": "https://github.com/owner/repo/pull/17#issuecomment-123"}
	default:
		pull := f.Pull
		pull.Draft = f.Draft
		out = pull
	}
	if len(args) > 0 && args[0] == "api" && strings.Contains(strings.Join(args, " "), "--include") {
		switch v := out.(type) {
		case []any:
			out = v[0]
		case [][]Comment:
			out = v[0]
		}
		body, _ := json.Marshal(out)
		etag := `"` + digest(string(body)) + `"`
		if strings.Contains(strings.Join(args, " "), "If-None-Match: "+etag) {
			fakeGHHeaders(stdout, "304 Not Modified", etag, f.Headers)
			return 0
		}
		fakeGHHeaders(stdout, "200 OK", etag, f.Headers)
	}
	if err = json.NewEncoder(stdout).Encode(out); err != nil {
		return 5
	}
	return 0
}

// fakeHeadBatch uses the same fixture as REST Pull reads.
func fakeHeadBatch(f fixture, args []string) any {
	query := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "query=") {
			query = strings.TrimPrefix(arg, "query=")
		}
	}
	data := map[string]any{}
	mergeability := f.Mergeability
	if mergeability == "" {
		mergeability = "MERGEABLE"
	}
	repos := regexp.MustCompile(`r[0-9]+:repository`).FindAllString(query, -1)
	pulls := regexp.MustCompile(`p([0-9]+):pullRequest\(number:([0-9]+)\)`).FindAllStringSubmatch(query, -1)
	for _, repo := range repos {
		nodes := map[string]any{"defaultBranchRef": map[string]string{"name": f.Pull.Base.Repo.DefaultBranch}}
		branches := regexp.MustCompile(`b([0-9]+):pullRequests\(first:1,headRefName:("(?:[^"\\]|\\.)*"),states:OPEN\)`).FindAllStringSubmatch(query, -1)
		for _, branch := range branches {
			var name string
			_ = json.Unmarshal([]byte(branch[2]), &name)
			var found []map[string]any
			for _, pull := range f.Discovery {
				if pull["headRefName"] == name {
					found = append(found, pull)
				}
			}
			nodes["b"+branch[1]] = map[string]any{"nodes": found}
		}
		for _, m := range pulls {
			n, _ := strconv.Atoi(m[2])
			if f.Fail != "" && strings.Contains(fmt.Sprintf("repos/owner/repo/pulls/%d", n), f.Fail) {
				nodes["p"+m[1]] = nil
				continue
			}
			state := strings.ToUpper(f.Pull.State)
			if f.Pull.Merged {
				state = "MERGED"
			}
			ci := "PENDING"
			ciState, _, _, _ := (Snapshot{Pull: f.Pull, Checks: f.Checks, Runs: f.Runs, Statuses: f.Statuses}).CIStatus()
			switch ciState {
			case "passed":
				ci = "SUCCESS"
			case "failed":
				ci = "FAILURE"
			}
			var contexts []map[string]string
			for _, check := range f.Checks {
				if check.Completed != "" {
					contexts = append(contexts, map[string]string{"completedAt": check.Completed})
				}
			}
			for _, status := range f.Statuses {
				if status.Updated != "" {
					contexts = append(contexts, map[string]string{"createdAt": status.Updated})
				}
			}
			nodes["p"+m[1]] = map[string]any{
				"number": n, "title": f.Pull.Title, "state": state, "merged": f.Pull.Merged, "isDraft": f.Draft, "mergeable": mergeability,
				"updatedAt": f.Pull.Updated, "headRefOid": f.Pull.Head.SHA, "headRefName": f.Pull.Head.Ref,
				"headRepository": map[string]string{"nameWithOwner": f.Pull.Head.Repo.FullName},
				"baseRefOid":     f.Pull.Base.SHA, "baseRefName": f.Pull.Base.Ref,
				"author":  map[string]string{"login": f.Pull.User.Login, "__typename": f.Pull.User.Type},
				"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": ci, "contexts": map[string]any{"nodes": contexts}}}}}},
			}
		}
		data[strings.TrimSuffix(repo, ":repository")] = nodes
	}
	return map[string]any{"data": data}
}

type testEnv struct {
	t           *testing.T
	s           *Service
	f           fixture
	file, calls string
	clock       time.Time
	author      subagent.Info
	sent        []Notice
	launches    []string
	reviewers   map[string]subagent.Info
}

// setPullHead gives one PR a distinct remote head in a multi-PR fixture.
func (e *testEnv) setPullHead(number int, head string) {
	e.t.Helper()
	pull := e.f.Pull
	pull.Number, pull.Head.SHA = number, head
	b, err := json.Marshal(pull)
	if err != nil {
		e.t.Fatal(err)
	}
	e.f.Raw[fmt.Sprintf("repos/owner/repo/pulls/%d", number)] = "HTTP/2.0 200 OK\r\n\r\n" + string(b)
	e.write()
}

// stallAfter is how long a test runs before stallStacks reports it.
const stallAfter = 5 * time.Second

// stallStacks prints every goroutine's stack when a test still runs after
// stallAfter, so a stall under CI load names what it waits on (issue
// msg_c6542fbcf0352b1d). It only reports: the test's result is unchanged.
func stallStacks(t *testing.T) {
	name := t.Name()
	timer := time.AfterFunc(stallAfter, func() {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		fmt.Fprintf(os.Stderr, "STALL: %s still runs after %s; goroutine stacks:\n%s\n", name, stallAfter, buf[:n])
	})
	t.Cleanup(func() { timer.Stop() })
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	stallStacks(t)
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	scoped, err := room.DefaultStore().Create(room.CreateInput{Name: "github-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, room.DefaultStore(), scoped.ID, "github.com", "owner/repo", "read-write")
	e := &testEnv{t: t, file: filepath.Join(dir, "fixture.json"), calls: filepath.Join(dir, "calls"), clock: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "gh")
	script := "#!/bin/sh\nexport GORACE=atexit_sleep_ms=0\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestFakeGH$ -- \"$@\"\n"
	if err = os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_FAKE_GH", "1")
	t.Setenv("PR_FIXTURE", e.file)
	t.Setenv("PR_CALLS", e.calls)
	t.Setenv("PR_POSTED", filepath.Join(dir, "posted"))
	e.author = subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", RoomID: scoped.ID, Room: scoped.Name, Dir: dir, ParentSession: "ses_20261017-000002-bbbbbbbb"}}
	// Lifecycle publication checks read durable metadata, not this fixture's Info.
	for _, id := range []string{e.author.ID, e.author.ParentSession, "ses_20261017-000003-cccccccc", "ses_20261017-000004-dddddddd"} {
		recordtest.Write(t, filepath.Join(dir, "sessions"), id, subagent.Meta{ID: id, RoomID: e.author.RoomID, Room: e.author.Room})
	}
	e.f = defaultFixture()
	e.s = &Service{Store: &Store{Root: filepath.Join(dir, "prs")}, GH: GitHub{Executable: fake, now: func() time.Time { return e.clock }}, Now: func() time.Time { return e.clock }}
	// Most watcher tests exercise HTTP parsing and lifecycle state, not process
	// startup. Keep the real command path for explicit subprocess tests and for
	// tests that replace Executable with a forbidden command.
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if cmd.Path != fake {
			return cmd.Run()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		env := cmd.Environ()
		getenv := func(key string) string {
			prefix := key + "="
			for i := len(env) - 1; i >= 0; i-- {
				if strings.HasPrefix(env[i], prefix) {
					return strings.TrimPrefix(env[i], prefix)
				}
			}
			return ""
		}
		if code := runFakeGH(cmd.Args[1:], cmd.Stdout, io.Discard, getenv); code != 0 {
			return fmt.Errorf("exit status %d", code)
		}
		return nil
	}
	e.reviewers = map[string]subagent.Info{}
	e.s.ReviewerProfiles = func() ([]profile.Profile, error) {
		return []profile.Profile{{Name: "judge", Taste: 7, Cost: 8, Intelligence: 9}, {Name: "reviewer", Taste: 5, Cost: 5}, {Name: "mock", Taste: 4, Cost: 4}}, nil
	}
	e.s.ReviewerInfo = func(id string) (subagent.Info, subagent.Turn, error) {
		info, ok := e.reviewers[id]
		if !ok {
			return info, subagent.Turn{}, fmt.Errorf("session missing")
		}
		return info, subagent.Turn{N: 1, Status: subagent.TurnRunning}, nil
	}
	e.s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		e.sent = append(e.sent, n)
		return n.ID, nil
	}
	e.s.LaunchReviewer = func(_ context.Context, _ Record, name, profile, task string) (string, error) {
		e.launches = append(e.launches, name+"/"+profile+"/"+task)
		id := fmt.Sprintf("ses_20261017-01000%d-cccccccc", len(e.launches)+2)
		e.reviewers[id] = subagent.Info{Meta: subagent.Meta{ID: id, RoomID: e.author.RoomID, Room: e.author.Room, Name: name, Role: "reviewer", Profile: profile, Turns: 1}, Status: subagent.StatusRunning, CurrentTurn: 1}
		return id, nil
	}
	e.s.CloseReviewer = closeFakeReviewer(e)
	e.write()
	return e
}

// defaultFixture is the fake GitHub state a watcher fixture starts from: one
// open pull request on main whose CI failed. Cases that need another state
// replace the fixture fields they care about.
func defaultFixture() fixture {
	f := fixture{Raw: map[string]string{}}
	f.Pull.Base.Ref = "main"
	f.Pull.Base.Repo.DefaultBranch = "main"
	f.Pull.State = "open"
	f.Pull.Number = 17
	f.Pull.User = User{Login: "owner", Type: "User"}
	f.Pull.Head.SHA = strings.Repeat("a", 40)
	f.Checks, f.Runs = ciFixture("failure", 1)
	return f
}

// ciFixture is the one check and workflow run a fake CI result has.
func ciFixture(conclusion string, attempt int) ([]Check, []Run) {
	return []Check{actionsCheck(Check{ID: int64(attempt), Name: "test / unit", Status: "completed", Conclusion: conclusion})},
		[]Run{{ID: 99, WorkflowID: 1, Name: "CI", Attempt: attempt, Status: "completed", Conclusion: conclusion}}
}

func (e *testEnv) setCI(conclusion string, attempt int) {
	e.f.Checks, e.f.Runs = ciFixture(conclusion, attempt)
}
func (e *testEnv) write() {
	e.t.Helper()
	b, _ := json.Marshal(e.f)
	if err := os.WriteFile(e.file, b, 0600); err != nil {
		e.t.Fatal(err)
	}
}
func (e *testEnv) track() Record {
	e.t.Helper()
	r, created, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, Ref{"github.com", "owner/repo", 17}, e.author, e.author.Dir)
	if err != nil || !created {
		e.t.Fatalf("track: %+v %v %v", r, created, err)
	}
	return r
}
func (e *testEnv) tick() {
	e.t.Helper()
	e.clock = e.clock.Add(time.Minute)
	e.write()
	if _, err := e.s.Tick(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}
func (e *testEnv) record() Record {
	e.t.Helper()
	r, err := e.s.Store.Get("github.com/owner/repo#17")
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}
func (e *testEnv) addTrustedComment(id int64, kind, body, login string) {
	e.addComment(id, kind, body, login)
	e.f.Comments[len(e.f.Comments)-1].Association = "COLLABORATOR"
}

func (e *testEnv) addComment(id int64, kind, body, login string) {
	e.f.Comments = append(e.f.Comments, Comment{ID: id, Kind: kind, Body: body, User: User{Login: login, Type: "User"}, Commit: e.f.Pull.Head.SHA, State: "COMMENTED", URL: fmt.Sprintf("https://github.com/owner/repo/pull/17#comment-%d", id)})
}

func TestTrackIdempotentAndKeepsOriginalAuthor(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	before, err := state.Revision(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	fakeGH := e.s.GH.Executable
	e.s.GH.Executable = "/no-gh-may-run-on-second-track"
	other := e.author
	other.ID = "other"
	got, created, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, refRecord(r), other, "different-dir")
	if err != nil || created || got.Author != r.Author || got.LookupDir != r.LookupDir {
		t.Fatalf("second track changed link: %+v %v %v", got, created, err)
	}
	after, _ := state.Revision(e.s.Store.Root)
	if before != after {
		t.Fatal("second track rewrote state")
	}
	e.s.GH.Executable = fakeGH
	if err = e.s.Untrack(context.Background(), r.Key); err != nil {
		t.Fatal(err)
	}
	got, created, err = e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, refRecord(r), e.author, e.author.Dir)
	if err != nil || created || got.Watching {
		t.Fatalf("track reactivated old link: %+v %v %v", got, created, err)
	}
	if err = e.s.Store.update(func(st *State) error { st.Records[r.Key].Polling.FullChecked = e.clock; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = e.s.Resume(r.Key); err != nil {
		t.Fatal(err)
	}
	if got := e.record(); !got.Watching || !got.Polling.FullChecked.IsZero() {
		t.Fatal("resume must force a full snapshot")
	}
}

func TestCIStreakBatchesLogsAndPausesOnlyCIWakes(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Log = "job output\nTOKEN=ghp_sensitive_token\nlast diagnostic line"
	e.track()
	e.tick()
	e.tick()
	if len(e.sent) != 1 || e.sent[0].Target != e.author.ID || !strings.Contains(e.sent[0].Text, "test / unit") || !strings.Contains(e.sent[0].Text, "last diagnostic line") || strings.Contains(e.sent[0].Text, "ghp_") {
		t.Fatalf("first CI batch: %+v", e.sent)
	}
	e.setCI("failure", 2)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	e.setCI("failure", 3)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	e.setCI("failure", 4)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	r := e.record()
	if len(e.sent) != 3 || e.sent[2].Target != e.author.ID || !r.CIPaused || !strings.Contains(r.PauseReason, "3 consecutive failures") {
		t.Fatalf("streak: %+v sent=%+v", r, e.sent)
	}
	// Stored author comments are noise. Owner comments on the very same login wake it.
	e.addTrustedComment(1, "issue-comments", Marker(e.author.ID)+" agent reply", "owner")
	if err := e.s.Store.update(func(st *State) error {
		if st.CommentSessions == nil {
			st.CommentSessions = map[string]string{}
		}
		st.CommentSessions["issue-comments:1"] = e.author.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.addTrustedComment(2, "issue-comments", "Rate limit exceeded", "coderabbitai[bot]")
	e.addTrustedComment(3, "issue-comments", "useful bot diagnostic", "other[bot]")
	e.addTrustedComment(4, "issue-comments", "Please inspect this too.", "owner")
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	e.tick()
	if len(e.sent) != 4 || !strings.Contains(e.sent[3].Text, "Please inspect") || !strings.Contains(e.sent[3].Text, "useful bot diagnostic") || strings.Contains(e.sent[3].Text, "agent reply") || strings.Contains(e.sent[3].Text, "Rate limit exceeded") {
		t.Fatalf("comment batch: %+v", e.sent)
	}
	if !e.record().CIPaused {
		t.Fatal("owner comment cleared CI pause")
	}
	if err := e.s.SetReviewEnabled(r.RoomID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Configure(r.RoomID, Reviewer{Profile: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	e.setCI("success", 5)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	e.tick()
	r = e.record()
	if r.CIPaused || r.Failures != 0 || len(e.launches) != 1 || r.Reviewer == "" {
		t.Fatalf("green transition: %+v launches=%+v", r, e.launches)
	}
}

func TestThirdCIFailureAndLandNoticeTargetTheAuthor(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	for attempt := 1; attempt <= 4; attempt++ {
		e.setCI("failure", attempt)
		e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
		e.tick()
	}
	if len(e.sent) != 3 || e.sent[2].Target != e.author.ID || e.sent[2].Kind != "ci" || !strings.Contains(e.sent[2].Text, "three times") {
		t.Fatalf("CI escalation: %+v", e.sent)
	}
	if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
		t.Fatal(err)
	}
	e.setCI("success", 5)
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	r = e.record()
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Ready to merge", 0); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 5 || e.sent[3].Target != "" || !strings.Contains(e.sent[3].Text, "approved") || strings.Contains(e.sent[3].Text, "ready to land") {
		t.Fatalf("approval routing: %+v", e.sent)
	}
	if e.sent[4].Target != e.author.ID || e.sent[4].Kind != "land" || !strings.Contains(e.sent[4].Text, "is ready to land at") {
		t.Fatalf("land notice: %+v", e.sent)
	}
}

// escalationEnv gives the watcher real sessions and a room. After track,
// every session is closed until a case opens it.
type escalationEnv struct {
	*testEnv
	rooms  *room.Store
	rec    room.Record
	mgr    *subagent.Manager
	starts []subagent.StartOptions
	fail   bool
}

func newEscalationEnv(t *testing.T) *escalationEnv {
	e := &escalationEnv{testEnv: newEnv(t), rooms: room.DefaultStore()}
	var err error
	if e.rec, err = e.rooms.Create(room.CreateInput{Name: "escalation"}); err != nil {
		t.Fatal(err)
	}
	e.mgr = &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(filepath.Dir(e.s.Store.Root), "sessions")}, Rooms: e.rooms, Spawner: nopSpawner{}, Settings: settings.Defaults,
		Profiles: func() (*profile.Store, error) {
			return &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}}}, nil
		}}
	e.s.Notify = notify.New(e.rooms, e.mgr)
	e.author.RoomID, e.author.Room = e.rec.ID, e.rec.Name
	e.session(e.author.ParentSession, "", true)
	e.session(e.author.ID, e.author.ParentSession, true)
	e.s.StartAgent = func(o subagent.StartOptions) (subagent.Info, error) {
		e.starts = append(e.starts, o)
		if e.fail {
			return subagent.Info{}, errors.New("no profile can start")
		}
		id := "ses_20261017-000009-eeeeeeee"
		e.session(id, "", true)
		return e.mgr.Store.Info(id)
	}
	return e
}

func (e *escalationEnv) session(id, parent string, open bool) {
	e.t.Helper()
	meta := subagent.Meta{ID: id, Name: id[len(id)-8:], Room: e.rec.Name, RoomID: e.rec.ID, ParentSession: parent, Profile: "mock", Resumable: true, HarnessSession: "h-" + id}
	if !open {
		meta.Closed = e.clock
	}
	dir := filepath.Join(e.mgr.Store.Root, id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		e.t.Fatal(err)
	}
	recordtest.Write(e.t, e.mgr.Store.Root, id, meta)
}

// track tracks the PR, then closes the whole author chain.
func (e *escalationEnv) track() {
	e.t.Helper()
	e.testEnv.track()
	e.session(e.author.ID, e.author.ParentSession, false)
	e.session(e.author.ParentSession, "", false)
}

// nopSpawner accepts a woken turn without running it.
type nopSpawner struct{}

func (nopSpawner) Spawn(_ *subagent.Manager, _ string, _ int, lock *os.File) error {
	return lock.Close()
}

// deliver sends one fresh notice and returns the address of the post.
func (e *escalationEnv) deliver(token, kindToken string) string {
	e.t.Helper()
	r := e.record()
	notice(&r, r.Author, "notice "+token, kindToken)
	n := r.Notices[len(r.Notices)-1]
	if _, err := e.s.deliver(context.Background(), r, n); err != nil {
		e.t.Fatal(err)
	}
	m, err := e.rooms.Message(e.rec.ID, n.ID)
	if err != nil || len(m.To) != 1 {
		e.t.Fatalf("post %s: %+v %v", token, m, err)
	}
	return m.To[0]
}

func TestNoticesWalkTheAuthorChain(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	parent, author := e.rec.Name+":"+e.author.ParentSession[len(e.author.ParentSession)-8:], e.rec.Name+":"+e.author.ID[len(e.author.ID)-8:]
	e.session(e.author.ID, e.author.ParentSession, true)
	e.session(e.author.ParentSession, "", true)
	if got := e.deliver("ci", "ci:2"); got != author {
		t.Fatalf("CI notice went to %s, want the idle author %s", got, author)
	}
	if got := e.deliver("land child", "land:a"); got != parent {
		t.Fatalf("child author's land notice went to %s, want the parent %s", got, parent)
	}
	e.session(e.author.ID, "", true)
	if got := e.deliver("land top", "land:b"); got != author {
		t.Fatalf("top-level author's land notice went to %s, want %s", got, author)
	}
	if len(e.starts) != 0 {
		t.Fatalf("an open chain started an agent: %+v", e.starts)
	}
}

func TestClosedChainStartsOneAgentAndMakesItTheAuthor(t *testing.T) {
	for _, tc := range []struct{ token, brief string }{{"ci:1", "Fix CI on PR #17"}, {"land:a", "Review the status of PR #17"}} {
		e := newEscalationEnv(t)
		e.track()
		agent := e.rec.Name + ":eeeeeeee"
		for i := range 2 {
			if got := e.deliver(tc.token+string(rune('a'+i)), tc.token+string(rune('a'+i))); got != agent {
				t.Fatalf("%s went to %s, want the started agent", tc.token, got)
			}
		}
		if len(e.starts) != 1 || e.starts[0].Room != e.rec.ID || !strings.HasPrefix(e.starts[0].Task, tc.brief) {
			t.Fatalf("%s starts: %+v", tc.token, e.starts)
		}
		if r := e.record(); r.Author != "ses_20261017-000009-eeeeeeee" || len(r.FormerAuthors) != 1 {
			t.Fatalf("started agent is not the author: %+v", r)
		}
	}
}

func TestOtherNoticesNeverStartAnAgent(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	if got := e.deliver("comment", "comment-batch"); got != e.rec.Name || len(e.starts) != 0 {
		t.Fatalf("comment notice went to %s with %d starts, want the owner and none", got, len(e.starts))
	}
}

func TestOwnerGetsTheNoticeWhenNoAgentCanStart(t *testing.T) {
	e := newEscalationEnv(t)
	e.fail = true
	e.track()
	if got := e.deliver("ci", "ci:1"); got != e.rec.Name {
		t.Fatalf("got %s, want the owner", got)
	}
	if len(e.starts) != 2 || e.starts[0].Profile != "mock" || e.starts[1].Profile != "" {
		t.Fatalf("starts: %+v", e.starts)
	}
}

func TestRootGetsTheNoticeWhenARecordWithoutDirCannotStart(t *testing.T) {
	e := newEscalationEnv(t)
	e.fail = true
	e.track()
	id := "ses_20261017-000008-ffffffff"
	e.session(id, "", true)
	meta := subagent.Meta{ID: id, Name: subagent.RoleRoot, Role: subagent.RoleRoot, Room: e.rec.Name, RoomID: e.rec.ID, Profile: "mock", Resumable: true, HarnessSession: "h-" + id}
	recordtest.Write(t, e.mgr.Store.Root, id, meta)
	root, err := e.mgr.Store.Info(id)
	if err != nil {
		t.Fatal(err)
	}
	r := e.record()
	r.Dir = ""
	notice(&r, r.Author, "notice ci", "ci:1")
	n := r.Notices[len(r.Notices)-1]
	if _, err := e.s.deliver(context.Background(), r, n); err != nil {
		t.Fatal(err)
	}
	// The post names root by its address; its To holds the room's root seat.
	if m, err := e.rooms.Message(e.rec.ID, n.ID); err != nil || !slices.Equal(m.Via, []string{"@" + root.Address()}) {
		t.Fatalf("post %+v %v; want it sent to root %s", m, err, root.Address())
	}
}

func TestVerdictsPushesAndMerge(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	e.s.Configure(r.RoomID, Reviewer{Profile: "judge"})
	e.tick()
	e.tick()
	r = e.record()
	if len(e.launches) != 1 || !strings.Contains(e.launches[0], "--head "+r.Head) || !strings.Contains(e.launches[0], "Read GitHub without posting") {
		t.Fatalf("review task: %+v", e.launches)
	}
	e.addTrustedComment(10, "reviews", Marker(r.Reviewer)+" Fix the parser.", "owner")
	e.addTrustedComment(11, "comments", Marker(r.Reviewer)+" Bounds check at line 7.", "owner")
	e.f.Comments[1].ReviewID = 10
	if err := e.s.Store.update(func(st *State) error {
		if st.CommentSessions == nil {
			st.CommentSessions = map[string]string{}
		}
		st.CommentSessions["reviews:10"] = r.Reviewer
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "Fix the parser") || !strings.Contains(e.sent[0].Text, "Bounds check at line 7") {
		t.Fatalf("kept review comments were not batched: %+v", e.sent)
	}
	e.write()
	ctx := context.Background()
	if err := e.s.Verdict(ctx, r.Key, e.author.ID, r.Head, "changes", "wrong reviewer", 10); err == nil {
		t.Fatal("author supplied reviewer verdict")
	}
	if err := e.s.Verdict(ctx, r.Key, r.Reviewer, r.Head, "changes", "Fix two bounds checks", 10); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 2 || e.sent[1].Target != e.author.ID || !strings.Contains(e.sent[1].Text, "Fix two bounds checks") || strings.Contains(e.sent[1].Text, "Fix the parser") {
		t.Fatalf("changes batch: %+v", e.sent)
	}
	if err := e.s.Verdict(ctx, r.Key, r.Reviewer, r.Head, "changes", "Fix two bounds checks", 10); err != nil {
		t.Fatal(err)
	}
	e.tick()
	if len(e.sent) != 2 {
		t.Fatal("replayed verdict or comments")
	}
	oldHead := r.Head
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.setCI("success", 2)
	e.write()
	if err := e.s.Verdict(ctx, r.Key, r.Reviewer, oldHead, "approve", "stale", 10); err == nil {
		t.Fatal("stale approval accepted")
	}
	e.tick()
	r = e.record()
	if r.Verdict != "" || r.Head == oldHead || len(e.launches) != 2 {
		t.Fatalf("push did not reset review: %+v", r)
	}
	if err := e.s.Verdict(ctx, r.Key, r.Reviewer, r.Head, "approve", "Ready\nto merge", 0); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 4 || e.sent[2].Target != "" || !strings.Contains(e.sent[2].Text, "approved") || e.sent[3].Target != e.author.ID || e.sent[3].Kind != "land" || !strings.Contains(e.sent[3].Text, "is ready to land at") {
		t.Fatalf("approve: %+v", e.sent)
	}
	e.f.Pull.State = "closed"
	e.f.Pull.Merged = true
	e.tick()
	r = e.record()
	if r.Watching || r.State != "merged" || len(e.sent) != 6 || e.sent[4].Target != e.author.ID || e.sent[4].Kind != "merged" || e.sent[5].Kind != "informational" {
		t.Fatalf("merge: %+v sent=%+v", r, e.sent)
	}
	calls, _ := os.ReadFile(e.calls)
	e.tick()
	after, _ := os.ReadFile(e.calls)
	if string(calls) != string(after) {
		t.Fatal("merged PR still queried GitHub")
	}
}

func TestManagedReviewerDefaultsOn(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.track()
	e.tick()
	e.tick()
	if len(e.launches) != 1 || e.record().Reviewer == "" {
		t.Fatal("automatic reviewer was not created")
	}
}

func TestPollErrorsDoNotLeakCredentialsOrStopWatching(t *testing.T) {
	e := newEnv(t)
	e.track()
	e.f.Fail = "/check-runs"
	e.write()
	if _, err := e.s.Tick(context.Background()); err == nil {
		t.Fatal("fake gh error lost")
	}
	r := e.record()
	if !r.Watching || r.Polling.Error == "" || r.Error != "" || strings.Contains(r.Polling.Error, "ghp_") {
		t.Fatalf("error state: %+v", r)
	}
	e.f.Fail = ""
	e.tick()
	if e.record().Polling.Error != "" {
		t.Fatal("poll did not recover")
	}
}

func TestOutboxRetryStoresOnlyOneRoomPost(t *testing.T) {
	e := newEnv(t)
	e.track()
	rooms := &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "outbox"})
	if err != nil {
		t.Fatal(err)
	}
	e.s.Notify = notify.New(rooms, subagent.NewManager())
	if err = e.s.Store.update(func(st *State) error {
		r := st.Records["github.com/owner/repo#17"]
		r.RoomID = rec.ID
		r.Watching = false
		notice(r, "", "a single durable batch", "same-event")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.s.Deliver = func(ctx context.Context, r Record, n Notice) (string, error) {
		calls++
		id, err := e.s.deliver(ctx, r, n)
		if err != nil {
			return id, err
		}
		if calls == 1 {
			return id, errors.New("lost acknowledgement")
		}
		return id, nil
	}
	if _, err = e.s.Tick(context.Background()); err == nil {
		t.Fatal("lost acknowledgement not reported")
	}
	if _, err = e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	posts, err := rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 || !e.record().Notices[0].Done {
		t.Fatalf("duplicate durable post: %+v %v", posts, err)
	}
}

func TestOwnerPRNoticeComesFromAuthorForUnreadAndOwnerWatches(t *testing.T) {
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "owner-alert"})
	if err != nil {
		t.Fatal(err)
	}
	meta := subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", RoomID: rec.ID, Room: rec.Name, Name: "author"}
	mgr := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}, Rooms: rooms}
	recordtest.Write(t, mgr.Store.Root, meta.ID, meta)
	s := &Service{Notify: notify.New(rooms, mgr)}
	r := Record{Key: "github.com/owner/repo#17", RoomID: rec.ID, Author: meta.ID, URL: "https://github.com/owner/repo/pull/17"}
	notice(&r, "", "Ready for owner review", "owner-approval")
	for i := 0; i < 2; i++ {
		if _, err = s.deliver(context.Background(), r, r.Notices[0]); err != nil {
			t.Fatal(err)
		}
	}
	posts, err := rooms.Messages(rec.ID)
	if err != nil || len(posts) != 1 || posts[0].From != meta.Address() || !posts[0].Addressed(rec.Name) || posts[0].Human || posts[0].Kind != "pr" {
		t.Fatalf("owner PR alert was hidden as an owner message: %+v %v", posts, err)
	}
}

func TestPollStoresTitleAndLatestCheckDetailsThroughMerge(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Title = "Fix parser"
	e.write()
	r := e.track()
	if r.Title != "Fix parser" || len(r.Checks) != 1 || r.Checks[0].State != "failure" {
		t.Fatalf("initial title/check view: %+v", r)
	}
	e.f.Pull.Title = "Fix parser bounds"
	e.f.Checks = []Check{{ID: 1, Name: "unit", Status: "completed", Conclusion: "failure"}, {ID: 2, Name: "unit", Status: "completed", Conclusion: "success", URL: "https://github.com/owner/repo/runs/2"}, {ID: 3, Name: "integration", Status: "in_progress"}}
	e.f.Statuses = []Status{{ID: 1, Context: "CodeRabbit", State: "success"}}
	e.f.Comments = []Comment{{ID: 91, Kind: "issue-comments", User: User{Login: "coderabbitai[bot]", Type: "Bot"}, Body: "Review in progress", URL: "https://github.com/owner/repo/pull/17#issuecomment-91", Updated: "2026-10-17T12:00:00Z"}}
	e.tick()
	r = e.record()
	if r.Title != e.f.Pull.Title || len(r.Checks) != 3 || r.Checks[0].Name != "CodeRabbit" || r.Checks[0].State != "success" || r.Checks[1].State != "pending" || r.Checks[2].State != "success" {
		t.Fatalf("latest check view: %+v", r.Checks)
	}
	if r.Checks[0].CommentURL != e.f.Comments[0].URL || r.Checks[0].URL != "" {
		t.Fatalf("missing bot check comment link: %+v", r.Checks[0])
	}
	e.f.Pull.State, e.f.Pull.Merged = "closed", true
	e.tick()
	if after := e.record(); after.State != "merged" || len(after.Checks) != 3 || after.Title != r.Title || after.Checks[0].CommentURL != r.Checks[0].CommentURL {
		t.Fatalf("merge lost title or last checks: %+v", after)
	}
}

func TestMarkedCommentAndSanitization(t *testing.T) {
	e := newEnv(t)
	t.Setenv("EXAMPLE_SECRET", "something-that-must-stay-private")
	text := "safe\nghp_secret123\nAuthorization: Bearer a-token\nEXAMPLE_SECRET=something-that-must-stay-private\neyJabc.def.ghi\nhttps://u:p@example.com"
	clean := Sanitize(text)
	for _, secret := range []string{"ghp_", "a-token", "something-that-must-stay-private", "eyJabc.def.ghi", "u:p@"} {
		if strings.Contains(clean, secret) {
			t.Fatalf("secret remains: %s", clean)
		}
	}
	if err := e.s.Comment(context.Background(), Ref{"github.com", "owner/repo", 17}, e.author.ID, "hello ghp_secret123", true); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(os.Getenv("PR_POSTED"))
	if !strings.Contains(string(body), "hello") || strings.Contains(string(body), "ghp_") || strings.Contains(string(body), "dyna:") {
		t.Fatalf("comment body must be sanitized and marker-free: %s", body)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.CommentSessions["issue-comments:123"] != e.author.ID {
		t.Fatalf("comment ID was not recorded for routing: %+v", st.CommentSessions)
	}
}

func TestLegacyMarkedCommentUsesCallerAndPreservesItsMarker(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	legacy := Marker(e.author.ID) + " an older agent reply"
	if err := e.s.Comment(context.Background(), refRecord(r), "ses_20261017-000009-zzzzzzzz", legacy, true); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(os.Getenv("PR_POSTED"))
	if !strings.Contains(string(body), legacy) {
		t.Fatalf("legacy marker missing from the comment: %s", body)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.CommentSessions["issue-comments:123"] != "ses_20261017-000009-zzzzzzzz" {
		t.Fatalf("authenticated caller session was not kept: %+v", st.CommentSessions)
	}
}

func TestKeptBotCommentsWakeInOneBatch(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Checks = nil
	e.f.Runs = nil
	e.track()
	e.addTrustedComment(31, "issue-comments", "meaningful automated finding", "coderabbitai[bot]")
	e.addTrustedComment(32, "issue-comments", "another finding", "other[bot]")
	e.tick()
	e.tick()
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "meaningful automated finding") || !strings.Contains(e.sent[0].Text, "another finding") {
		t.Fatalf("kept comments not batched: %+v", e.sent)
	}
}

func TestNamedReviewerRejectsUnboundAndStaleVerdicts(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	e.s.Configure(r.RoomID, Reviewer{Profile: "judge"})
	e.tick()
	actor := e.record().Reviewer
	if err := e.s.Verdict(context.Background(), r.Key, actor, "", "approve", "unbound", 0); err == nil {
		t.Fatal("named reviewer approved without an assigned head")
	}
	e.f.Pull.Head.SHA = strings.Repeat("c", 40)
	e.write()
	if err := e.s.Verdict(context.Background(), r.Key, actor, r.Head, "approve", "old head", 0); err == nil {
		t.Fatal("named reviewer approved an old head")
	}
}

func TestURLFormsResolveToOnePR(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	ref, err := e.s.GH.Resolve(context.Background(), "https://GitHub.com/OWNER/REPO/pull/17#discussion_r123", e.author.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Key() != r.Key {
		t.Fatalf("case or fragment changed PR key: %q != %q", ref.Key(), r.Key)
	}
	e.s.GH.Executable = "/idempotent-track-must-not-query-gh"
	got, created, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, ref, e.author, e.author.Dir)
	if err != nil || created || got.Key != r.Key {
		t.Fatalf("duplicate URL tracking: %+v %v %v", got, created, err)
	}
}

func TestPRNumberUsesCurrentRepositoryAfterTrackingAnotherRepo(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	if err := e.s.Store.update(func(st *State) error {
		delete(st.Records, r.Key)
		r.Repo = "another/repository"
		r.Key = refRecord(r).Key()
		st.Records[r.Key] = &r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref, err := e.s.Resolve(context.Background(), "17", e.author.Dir)
	if err != nil || ref.Key() != "github.com/owner/repo#17" {
		t.Fatalf("a foreign tracked PR captured the local number: %+v %v", ref, err)
	}
}

func TestPendingOutboxRetriesEvenWhenGitHubIsUnavailable(t *testing.T) {
	e := newEnv(t)
	e.track()
	attempts := 0
	e.s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		attempts++
		if attempts == 1 {
			return n.ID, errors.New("temporary delivery failure")
		}
		return n.ID, nil
	}
	e.write()
	if _, err := e.s.Tick(context.Background()); err == nil {
		t.Fatal("delivery failure not reported")
	}
	e.f.Fail = "/pulls/17"
	e.clock = e.clock.Add(time.Minute)
	e.write()
	if _, err := e.s.Tick(context.Background()); err == nil {
		t.Fatal("GitHub outage not reported")
	}
	if attempts != 2 || !e.record().Notices[0].Done {
		t.Fatalf("outbox stopped with GitHub: attempts=%d record=%+v", attempts, e.record())
	}
}

func TestGreenCILaunchesOneActualMockReviewerSession(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	e.author.RoomID = rec.ID
	e.setCI("success", 1)
	e.track()
	mgr := subagent.NewManager()
	mgr.Rooms = rooms
	mgr.Spawner = subagent.InProcessSpawner{Ctx: ctx}

	mgr.Profiles = func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{{Name: "judge", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 5}}}, nil
	}
	mgr.Settings = settings.Defaults
	e.s.Notify = notify.New(rooms, mgr)
	e.s.LaunchReviewer = nil
	e.s.ReviewerProfiles = nil
	e.s.ReviewerInfo = nil
	e.s.CloseReviewer = nil
	e.s.Configure(rec.ID, Reviewer{Profile: "judge"})
	e.tick()
	e.tick()
	r := e.record()
	if r.Reviewer == "" {
		t.Fatalf("reviewer not assigned: %+v", r)
	}
	waitCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	info, turn, err := mgr.Wait(waitCtx, r.Reviewer, 1)
	if err != nil || !info.Service || info.RoomID != rec.ID || info.Profile != "judge" || !strings.Contains(turn.Prompt, r.URL) || !strings.Contains(turn.Prompt, r.Head) {
		t.Fatalf("review session: %+v %+v %v", info, turn, err)
	}
	all, err := mgr.Store.List(false)
	if err != nil || len(all) != 1 {
		t.Fatalf("reviewers: %+v %v", all, err)
	}
}

func TestCIRerunKeepsUnfinishedReview(t *testing.T) {
	for _, named := range []bool{false} {
		t.Run(fmt.Sprintf("named=%t", named), func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
			r := e.track()
			cfg := Reviewer{Profile: "judge"}
			if err := e.s.Configure(r.RoomID, cfg); err != nil {
				t.Fatal(err)
			}
			e.tick()
			assigned := e.record()
			launches, notices := len(e.launches), len(e.sent)
			if assigned.Round != 1 || assigned.Reviewer == "" || assigned.Verdict != "" {
				t.Fatalf("initial assignment: %+v", assigned)
			}
			e.setCI("success", 3)
			e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
			e.tick()
			e.tick()
			got := e.record()
			if got.CIKey == assigned.CIKey || got.Round != assigned.Round || got.Reviewer != assigned.Reviewer || got.ReviewerName != assigned.ReviewerName || len(e.launches) != launches || len(e.sent) != notices {
				t.Fatalf("rerun changed assignment: before=%+v after=%+v launches=%d notices=%d", assigned, got, len(e.launches), len(e.sent))
			}
			e.f.Pull.Head.SHA = strings.Repeat("b", 40)
			e.tick()
			got = e.record()
			if got.Round != assigned.Round+1 || got.RoundHead == assigned.RoundHead || got.ReviewerName != assigned.ReviewerName {
				t.Fatalf("new head did not start a round: %+v", got)
			}
			if len(e.sent) != notices || len(e.launches) != launches+1 {
				t.Fatalf("new head assignment: launches=%d notices=%d", len(e.launches), len(e.sent))
			}
			if err := e.s.Verdict(context.Background(), r.Key, got.Reviewer, got.Head, "approve", "Ready", 0); err != nil {
				t.Fatalf("verdict with passing CI: %v", err)
			}
		})
	}
}

func TestSharedGitHubFixtureDoesNotStartProcesses(t *testing.T) {
	e := newEnv(t)
	if err := os.Remove(e.s.GH.Executable); err != nil {
		t.Fatal(err)
	}
	r := e.track()
	if r.Head != e.f.Pull.Head.SHA {
		t.Fatal("fixture lost the published snapshot")
	}
}

func TestGitHubProcessBoundary(t *testing.T) {
	e := newEnv(t)
	gh := e.s.GH
	gh.run = nil // Exercise exec and its output handling, without the shared fixture shortcut.
	ctx := context.Background()
	t.Run("snapshot", func(t *testing.T) {
		want, err := e.s.GH.Pull(ctx, Ref{"github.com", "owner/repo", 17})
		if err != nil {
			t.Fatal(err)
		}
		got, err := gh.Pull(ctx, Ref{"github.com", "owner/repo", 17})
		if err != nil || got.Head.SHA != want.Head.SHA {
			t.Fatalf("snapshot: %+v %v", got, err)
		}
	})
	t.Run("stderr redaction", func(t *testing.T) {
		e.f.Fail = "pulls/17"
		e.write()
		raw, err := gh.command(ctx, "", false, "api", "repos/owner/repo/pulls/17", "--include")
		if err == nil || len(raw) != 0 || strings.Contains(err.Error(), "ghp_") {
			t.Fatalf("stderr escaped: %q %v", raw, err)
		}
		e.f.Fail = ""
		e.write()
	})
	t.Run("response limit", func(t *testing.T) {
		e.f.Blobs = map[string][]byte{"repos/owner/repo/blob": []byte(strings.Repeat("x", 256*1024))}
		e.write()
		raw, err := gh.commandLimit(ctx, "", false, 3, "api", "repos/owner/repo/blob")
		if !errors.Is(err, errGHResponseLimit) || raw != nil {
			t.Fatalf("unbounded response: %q %v", raw, err)
		}
	})
	t.Run("log tail", func(t *testing.T) {
		e.f.Log = strings.Repeat("x", 9000) + "last line"
		e.write()
		raw, err := gh.command(ctx, "", true, "run", "view", "99", "--repo", "owner/repo", "--log-failed")
		if err != nil || len(raw) != 8000 || !strings.HasSuffix(string(raw), "last line") {
			t.Fatalf("log tail: %d bytes %v", len(raw), err)
		}
	})
	t.Run("cancelled context", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := gh.command(cancelled, "", false, "api", "repos/owner/repo/pulls/17"); !errors.Is(err, context.Canceled) {
			t.Fatalf("ignored cancellation: %v", err)
		}
	})
}

func TestSharedGitHubFixtureUsesCommandEnvironment(t *testing.T) {
	e := newEnv(t)
	cmd := exec.Command(e.s.GH.Executable, "api", "repos/owner/repo/pulls/17", "--include")
	cmd.Env = os.Environ()
	var output strings.Builder
	cmd.Stdout = &output
	t.Setenv("PR_FIXTURE", filepath.Join(t.TempDir(), "missing"))
	if err := e.s.GH.run(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), e.f.Pull.Head.SHA) {
		t.Fatal("fixture ignored the command environment")
	}
}

func (e *testEnv) landNotices() (out []Notice) {
	for _, n := range e.sent {
		if strings.Contains(n.Text, "is ready to land at") {
			out = append(out, n)
		}
	}
	return out
}

func TestLandNoticeFiresWhicheverComesLast(t *testing.T) {
	for _, ciFirst := range []bool{false, true} {
		name := "verdict then CI"
		if ciFirst {
			name = "CI then verdict"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			r := e.track()
			e.s.Configure(r.RoomID, Reviewer{Profile: "judge"})
			e.tick()
			e.tick()
			r = e.record()
			if r.Reviewer == "" {
				t.Fatalf("no reviewer assigned: %+v", r)
			}
			if !ciFirst {
				// A source verdict may arrive while GitHub CI is pending.
				e.f.Checks = []Check{actionsCheck(Check{ID: 2, Name: "test / unit", Status: "in_progress"})}
				e.f.Runs = []Run{{ID: 99, WorkflowID: 1, Name: "CI", Attempt: 2, Status: "in_progress"}}
				e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
				e.tick()
				if e.record().CI != "pending" {
					t.Fatalf("CI is %q, want pending", e.record().CI)
				}
			}
			if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "approve", "Ready", 0); err != nil {
				t.Fatal(err)
			}
			if !ciFirst {
				if n := e.landNotices(); len(n) != 0 {
					t.Fatalf("notice sent before CI passed: %+v", n)
				}
				e.setCI("success", 2)
				e.f.Pull.Updated = e.clock.Add(2 * time.Minute).Format(time.RFC3339)
				e.tick()
			}
			e.tick()
			e.tick()
			got := e.landNotices()
			if len(got) != 1 || got[0].Target != e.author.ID || got[0].Kind != "land" || !strings.Contains(got[0].Text, "ready to land at "+r.Head) || strings.Contains(got[0].Text, "Land it with:") {
				t.Fatalf("land notices: %+v", got)
			}
		})
	}
}

// Root, the room owner, tracks a PR with no session. Its notices go to the
// owner (no target), so no agent starts for them.
func TestRootAuthorTracksAndGetsOwnerNotices(t *testing.T) {
	e := newEnv(t)
	e.f.Log = "job output\nlast diagnostic line"
	e.author.ID = ""
	r := e.track()
	if r.Author != "" || r.RoomID != e.author.RoomID {
		t.Fatalf("root record: author %q room %q", r.Author, r.RoomID)
	}
	e.tick()
	e.tick()
	if len(e.sent) != 1 || e.sent[0].Target != "" || !strings.Contains(e.sent[0].Text, "last diagnostic line") {
		t.Fatalf("root CI notice: %+v", e.sent)
	}
}

func fakeGHHeaders(out io.Writer, status, etag string, headers map[string]string) {
	fmt.Fprint(out, "HTTP/2.0 "+status+"\r\n")
	if etag != "" {
		fmt.Fprint(out, "ETag: "+etag+"\r\n")
	}
	for key, value := range headers {
		fmt.Fprint(out, key+": "+value+"\r\n")
	}
	fmt.Fprint(out, "\r\n")
}
