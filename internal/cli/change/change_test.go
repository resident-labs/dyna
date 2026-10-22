package change

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

type fixture struct {
	t                *testing.T
	s                *watch.Service
	author, reviewer subagent.Info
	dir, head, room  string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	storetest.Isolate(t)
	for _, key := range []string{"DYNA_AGENT_JOURNAL", "DYNA_AGENT_JOURNAL_ROOT", "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "MUSE_SESSION_ID"} {
		t.Setenv(key, "")
	}
	f := &fixture{t: t, dir: t.TempDir()}
	rooms := room.DefaultStore()
	r, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	f.room = r.ID
	f.author = subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", Name: "author", Profile: "mock", Resumable: true, HarnessSession: "fixture-author", RoomID: r.ID, Room: r.Name, Dir: f.dir}}
	f.reviewer = f.author
	f.reviewer.ID = "ses_20261017-000002-bbbbbbbb"
	f.reviewer.Name = "reviewer"
	f.s = watch.New()
	f.s.Notify = notify.New(rooms, subagent.NewManager())
	// close, reply and policy are engine calls: the fake engine serves them
	// from a pr/view service over this same PR store.
	view := &prview.Service{Notify: f.s.Notify,
		SessionInRoom: func(roomRef, ref string) (subagent.Info, error) {
			switch ref {
			case "":
				return subagent.Info{Meta: subagent.Meta{RoomID: r.ID, Room: r.Name, Dir: f.dir}}, nil
			case f.author.ID:
				return f.author, nil
			case f.reviewer.ID:
				return f.reviewer, nil
			}
			return subagent.Info{}, fmt.Errorf("unknown session %s", ref)
		},
		WorkspaceRoot: func(string, string) (string, error) { return f.dir, nil },
	}
	f.s.Store = view.Watcher().Store
	engineCalls, err := websvc.Bind([]websvc.Binding{
		{Name: "RecordPRVerdict", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.RecordPRVerdict},
		{Name: "CloseChange", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.CloseChange},
		{Name: "OpenChange", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.OpenChange},
		{Name: "ReplyToFinding", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.ReplyToFinding},
		{Name: "SetChangePolicyOverride", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.SetChangePolicyOverride},
		{Name: "SetRoomChangePolicy", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.SetRoomChangePolicy},
		{Name: "SetGitHubAccess", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.SetGitHubAccess},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), engineCalls)
	f.s.GH.Executable = "/must-not-contact-github"
	if err := os.MkdirAll(f.s.Store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, info := range []subagent.Info{f.author, f.reviewer} {
		recordtest.Write(t, filepath.Join(filepath.Dir(f.s.Store.Root), "sessions"), info.ID, info.Meta)
	}
	f.git("init", "-b", "main")
	f.git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "initial")
	f.head = strings.TrimSpace(f.git("rev-parse", "HEAD"))
	f.git("checkout", "-b", "topic")
	// The room's repository is the test worktree: the engine opens a change
	// only from a checkout of it.
	if err := rooms.UpdatePreferences(r.ID, func(rec *room.Record) error { rec.Repo = f.dir; return nil }); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return f
}
func (f *fixture) git(args ...string) string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", f.dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %s %v", args, b, err)
	}
	return string(b)
}
func (f *fixture) call(input string, args ...string) (string, error) {
	f.t.Helper()
	cmd := Command(Options{Service: func() *watch.Service { return f.s }, Session: func(ref string) (subagent.Info, error) {
		switch ref {
		case f.author.ID:
			return f.author, nil
		case f.reviewer.ID:
			return f.reviewer, nil
		}
		return subagent.Info{}, fmt.Errorf("unknown session %s", ref)
	}})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// as stamps the process as session id, as Dyna does for its agents.
func (f *fixture) as(id string) {
	f.t.Setenv(mode.EnvMode, mode.Subagent)
	f.t.Setenv(mode.EnvSession, id)
	f.t.Setenv(mode.EnvRoomID, f.room)
}

// root removes the stamp, so commands act as root, the room owner.
func (f *fixture) root() {
	storetest.ClearStamp(f.t)
}
func (f *fixture) write(st watch.State) {
	f.t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		f.t.Fatal(err)
	}
	var stored state.State
	if err := json.Unmarshal(b, &stored); err != nil {
		f.t.Fatal(err)
	}
	if err := state.Write(f.s.Store.Root, stored); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) seed() watch.Record {
	r := watch.Record{Key: "change_test", ChangeID: "generic-id", Kind: "local", Author: f.author.ID, Reviewer: f.reviewer.ID, RoomID: f.room, Head: f.head, RoundHead: f.head, Round: 1, RepoPath: filepath.Join(f.dir, ".git"), Branch: "topic", Base: f.head, BaseRef: "main", Watching: true, State: "open", Findings: []watch.Finding{{ID: "f1", Severity: "nit", File: "a.go", Line: 1, Why: "name is unclear", Fix: "rename it"}}}
	f.write(watch.State{Version: 1, Records: map[string]*watch.Record{r.Key: &r}, Reviewers: map[string]watch.Reviewer{}})
	return r
}
func (f *fixture) file(name, text string) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestOpenNotesAndStampedIdentity(t *testing.T) {
	storetest.Isolate(t)
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, dir: t.TempDir(), room: rec.ID, s: watch.New()}
	f.author = subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", RoomID: rec.ID, Room: rec.Name}}
	f.reviewer = f.author
	f.reviewer.ID = "ses_20261017-000002-bbbbbbbb"
	f.s.Notify = notify.New(rooms, subagent.NewManager())
	t.Chdir(f.dir)
	calls := 0
	bindings, err := websvc.Bind([]websvc.Binding{{Name: "OpenChange", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, roomID, author, pr, dir string, opts watch.OpenChangeOptions) (prview.OpenedChange, error) {
		calls++
		if roomID != f.room || author != f.author.ID || pr != "" || dir != f.dir {
			t.Errorf("open arguments: room=%q author=%q pr=%q dir=%q", roomID, author, pr, dir)
		}
		wantNotes := (watch.ChangeNotes{Intent: "Handle input", Testing: "Unit checks", Risks: "None", Questions: "Check names"})
		if opts.Base != "main" || opts.Title != "Fix parser" || opts.Notes != wantNotes || opts.Category != "fix" || len(opts.Dependencies) != 2 || opts.Dependencies[0] != "dep,a" || opts.Dependencies[1] != "dep-b" {
			t.Errorf("open options: %+v", opts)
		}
		return prview.OpenedChange{Change: watch.Record{Key: "engine-change", Author: author, RoomID: roomID}, Created: true}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), bindings)
	f.as(f.author.ID)
	out, err := f.call("", "open", "--base", "main", "--title", "Fix parser", "--intent", "Handle input", "--testing", "Unit checks", "--risks", "None", "--questions", "Check names", "--category", "fix", "--depends-on", "dep,a", "--depends-on", "dep-b", "--room", "work")
	if err != nil {
		t.Fatalf("open: %s %v", out, err)
	}
	var got prview.OpenedChange
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Created || got.Change.Key != "engine-change" || got.Change.Author != f.author.ID || got.Change.RoomID != f.room {
		t.Fatalf("open result: %+v", got)
	}
	if _, err := f.call("", "open", "--session", f.reviewer.ID); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("accepted session override: %v", err)
	}
	other, err := rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("", "open", "--room", other.ID); err == nil {
		t.Fatal("accepted room override")
	}
	if _, err := f.call("", "open", "--category", "unknown"); err == nil {
		t.Fatal("accepted invalid category")
	}
	if calls != 1 {
		t.Fatalf("engine calls = %d; invalid commands must not reach the engine", calls)
	}
}

func TestChangeShowReplyAndClose(t *testing.T) {
	f := setup(t)
	r := f.seed()
	// Root with no --room lists the current repo's room, which is "work".
	if out, err := f.call("", "list"); err != nil || !strings.Contains(out, r.Key) {
		t.Fatalf("root list: %s %v", out, err)
	}
	if out, err := f.call("", "show", r.ChangeID); err != nil || !strings.Contains(out, r.Key) {
		t.Fatalf("show: %s %v", out, err)
	}
	f.as(f.reviewer.ID)
	if _, err := f.call("", "reply", r.ChangeID, "f1", "fixed", "--message", "Renamed"); err == nil {
		t.Fatal("accepted reviewer reply")
	}
	f.as(f.author.ID)
	if out, err := f.call("", "reply", r.ChangeID, "f1", "fixed", "--message", "Renamed"); err != nil || !strings.Contains(out, "Renamed") {
		t.Fatalf("reply: %s %v", out, err)
	}
	f.as(f.author.ID)
	if out, err := f.call("", "close", r.ChangeID); err != nil || !strings.Contains(out, `"state": "closed"`) {
		t.Fatalf("close: %s %v", out, err)
	}
}

func TestVerdictWithFindingsAndMarkdown(t *testing.T) {
	for _, value := range []string{"approve", "approve-with-nits", "changes", "question"} {
		t.Run(value, func(t *testing.T) {
			f := setup(t)
			r := f.seed()
			markdown := "# Review\n\nKeep this full review.\n"
			findings := f.file("findings.json", `[{"id":"f2","severity":"nit","file":"a.go","line":2,"why":"unclear","fix":"rename"}]`)
			f.as(f.reviewer.ID)
			out, err := f.call(markdown, "verdict", r.ChangeID, value, "--head", f.head, "--body-file", "-", "--findings-file", findings)
			if err != nil {
				t.Fatalf("verdict: %s %v", out, err)
			}
			got, err := f.s.Store.Get(r.Key)
			if err != nil {
				t.Fatal(err)
			}
			if got.Summary != markdown || got.Verdict != value || len(got.Findings) != 1 || got.Findings[0].ID != "f2" || len(got.ReviewRounds) != 1 {
				t.Fatalf("saved review: %+v", got)
			}
			if !json.Valid([]byte(out)) {
				t.Fatalf("invalid JSON: %s", out)
			}
		})
	}
}

func TestVerdictRejectsOversizedAndInvalidInput(t *testing.T) {
	f := setup(t)
	r := f.seed()
	f.as(f.reviewer.ID)
	args := []string{"verdict", r.ChangeID, "question", "--head", f.head}
	cases := []struct {
		name, input string
		extra       []string
	}{
		{"large stdin", strings.Repeat("x", watch.MaxReviewBytes+1), []string{"--body-file", "-"}},
		{"large file", "", []string{"--body-file", f.file("large.md", strings.Repeat("x", watch.MaxReviewBytes+1))}},
		{"large positional", "", []string{strings.Repeat("x", watch.MaxReviewBytes+1)}},
		{"large JSON", "", []string{"Review", "--findings-file", f.file("large.json", "[]"+strings.Repeat(" ", watch.MaxFindingsBytes))}},
		{"object", "", []string{"Review", "--findings-file", f.file("object.json", "{}")}},
		{"null", "", []string{"Review", "--findings-file", f.file("null.json", "null")}},
		{"trailing JSON", "", []string{"Review", "--findings-file", f.file("trailing.json", "[] []")}},
		{"conflicting body", "text", []string{"Review", "--body-file", "-"}},
		{"two stdin readers", "text", []string{"--body-file", "-", "--findings-file", "-"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := f.call(tc.input, append(append([]string{}, args...), tc.extra...)...); err == nil {
				t.Fatalf("accepted input: %s", out)
			}
		})
	}
	f.as(f.reviewer.ID)
	if _, err := f.call("", "verdict", r.ChangeID, "approve", "Review"); err == nil {
		t.Fatal("accepted missing head")
	}
	f.as(f.reviewer.ID)
	if _, err := f.call("", "verdict", r.ChangeID, "approve", "Review", "--head", " "); err == nil {
		t.Fatal("accepted empty head")
	}
	got, err := f.s.Store.Get(r.Key)
	if err != nil || got.Verdict != "" {
		t.Fatalf("invalid review changed state: %+v %v", got, err)
	}
}

func TestBoundedReadersAcceptExactLimits(t *testing.T) {
	f := setup(t)
	r := f.seed()
	body := strings.Repeat("x", watch.MaxReviewBytes)
	findings := f.file("findings.json", "[]"+strings.Repeat(" ", watch.MaxFindingsBytes-2))
	f.as(f.reviewer.ID)
	out, err := f.call("", "verdict", r.ChangeID, "question", "--head", f.head, "--body-file", f.file("review.md", body), "--findings-file", findings)
	if err != nil {
		t.Fatalf("exact limits: %s %v", out, err)
	}
	got, err := f.s.Store.Get(r.Key)
	if err != nil || got.Summary != body {
		t.Fatalf("body was clipped: %d %v", len(got.Summary), err)
	}
}

func TestOpenPRUpdatesNotesWithoutPublishing(t *testing.T) {
	f := setup(t)
	r := watch.Record{Key: "github.com/owner/repo#17", ChangeID: "existing-change", Kind: "github", Author: f.author.ID, RoomID: f.room, Watching: true, State: "open"}
	f.write(watch.State{Version: 1, Records: map[string]*watch.Record{r.Key: &r}})
	f.as(f.author.ID)
	out, err := f.call("", "open", "--pr", "https://github.com/owner/repo/pull/17", "--intent", "Fix parsing", "--category", "small", "--depends-on", "first")
	if err != nil || !strings.Contains(out, `"created": false`) {
		t.Fatalf("PR notes: %s %v", out, err)
	}
	got, err := f.s.Store.Get(r.ChangeID)
	if err != nil || got.Notes.Intent != "Fix parsing" || got.Category != "small" || len(got.Dependencies) != 1 {
		t.Fatalf("PR notes: %+v %v", got, err)
	}
}

func TestPRVerdictHonorsPublishingAndNeverPosts(t *testing.T) {
	f := setup(t)
	writes := filepath.Join(t.TempDir(), "writes")
	t.Setenv("CHANGE_TEST_WRITES", writes)
	t.Setenv("CHANGE_TEST_HEAD", f.head)
	t.Setenv("CHANGE_TEST_REVIEWER", f.reviewer.ID)
	script := `#!/bin/sh
case "$*" in *--include*) printf 'HTTP/2.0 200 OK\r\n\r\n';; esac
case "$*" in
 *"graphql"*) printf '{"data":{"r0":{"defaultBranchRef":{"name":"main"},"p17":{"number":17,"state":"OPEN","headRefOid":"%s","mergeable":"MERGEABLE","isDraft":false}}}}' "$CHANGE_TEST_HEAD"; exit 0;;
 *"--method POST"*|*"pr review"*|*"pr comment"*) printf '%s\n' "$*" >> "$CHANGE_TEST_WRITES"; exit 1;;
esac
case "$*" in
 *"repo view"*) printf '{"nameWithOwner":"owner/repo","url":"https://github.com/owner/repo"}';;
 *"check-runs?"*) printf '{"check_runs":[{"name":"unit","status":"completed","conclusion":"success"}]}';;
 *"actions/runs?"*) printf '{"workflow_runs":[]}';;
 *"/status?"*) printf '{"statuses":[]}';;
 *"/reviews?"*) printf '[{"id":42,"body":"Review <!-- dyna:%s -->","commit_id":"%s","state":"COMMENTED"}]' "$CHANGE_TEST_REVIEWER" "$CHANGE_TEST_HEAD";;
 *"/comments?"*) printf '[]';;
 *) printf '{"state":"open","head":{"sha":"%s"}}' "$CHANGE_TEST_HEAD";;
esac
`
	fake := f.file("gh", script)
	if err := os.Chmod(fake, 0700); err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, f.s.Notify.Rooms, f.room, "github.com", "owner/repo", "read-write")
	f.s.GH.Rooms = f.s.Notify.Rooms
	f.s.GH.Executable = fake
	// The engine's own watcher, which links the PR, uses it too.
	t.Setenv(ghclient.BinaryEnv, fake)
	f.as(f.author.ID)
	if out, err := f.call("", "open", "--pr", "17", "--intent", "New PR notes"); err != nil || !strings.Contains(out, `"created": true`) || !strings.Contains(out, "New PR notes") {
		t.Fatalf("open new PR by number: %s %v", out, err)
	}
	for _, publishing := range []string{watch.PublishingLocal, watch.PublishingGitHub} {
		r := watch.Record{Key: "github.com/owner/repo#17", ChangeID: "generic-pr", Kind: "github", Host: "github.com", Repo: "owner/repo", Number: 17, Author: f.author.ID, Reviewer: f.reviewer.ID, RoomID: f.room, Head: f.head, RoundHead: f.head, Round: 1, Watching: true, State: "open", ReviewPublishing: publishing}
		f.write(watch.State{Version: 1, Records: map[string]*watch.Record{r.Key: &r}, Reviewers: map[string]watch.Reviewer{}})
		f.as(f.reviewer.ID)
		args := []string{"verdict", r.ChangeID, "question", "Review", "--head", f.head}
		if publishing == watch.PublishingGitHub {
			if _, err := f.call("", args...); err == nil || !strings.Contains(err.Error(), "--review-id") {
				t.Fatalf("GitHub mode accepted missing ID: %v", err)
			}
			args = append(args, "--review-id", "42")
		}
		if out, err := f.call("", args...); err != nil {
			t.Fatalf("%s verdict: %s %v", publishing, out, err)
		}
	}
	if _, err := os.Stat(writes); !os.IsNotExist(err) {
		t.Fatalf("CLI wrote to GitHub: %v", err)
	}
}

func TestPolicyAndSkipReasons(t *testing.T) {
	f := setup(t)
	if _, err := f.call("", "policy", "--room", f.room, "--checks", ""); err == nil {
		t.Fatal("accepted blank check policy")
	}
	f.as(f.author.ID)
	if _, err := f.call("", "open", "--skip-checks", ""); err == nil {
		t.Fatal("accepted blank skip reason")
	}
	f.root()
	if _, err := f.call("", "policy", "--room", f.room, "--review", "off"); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.RoomChangePolicy(f.room)
	if err != nil || p.Review != "off" {
		t.Fatalf("room policy %+v %v", p, err)
	}
	f.as(f.author.ID)
	if _, err = f.call("", "open", "--base", "main", "--skip-review", "Documentation only"); err != nil {
		t.Fatal(err)
	}
	f.root()
	rows, err := f.s.Changes(f.room)
	if err != nil || len(rows) != 1 {
		t.Fatalf("changes %+v %v", rows, err)
	}
	r := rows[0]
	if r.PolicyOverride.Review != "off" || r.PolicyOverride.Reason != "Documentation only" {
		t.Fatalf("skip not recorded: %+v", r.PolicyOverride)
	}
	if _, err = f.call("", "policy", r.Key, "--review", "off"); err == nil {
		t.Fatal("accepted override with no reason")
	}
	if _, err = f.call("", "policy", r.Key, "--review", "on", "--reason", "Review requested"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.call("", "policy", r.Key, "--reset"); err != nil {
		t.Fatal(err)
	}
	r, err = f.s.Store.Get(r.Key)
	if err != nil || !r.PolicyOverride.Empty() {
		t.Fatalf("reset %+v %v", r.PolicyOverride, err)
	}
}

// Root, with no Dyna stamp, opens, replies to and closes changes with no flag.
func TestRootActsWithoutStamp(t *testing.T) {
	f := setup(t)
	out, err := f.call("", "open", "--base", "main", "--room", "work")
	if err != nil {
		t.Fatalf("root open: %s %v", out, err)
	}
	var got struct {
		Change watch.Record `json:"change"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Change.Author != "" || got.Change.RoomID != f.room {
		t.Fatalf("root change: author %q room %q", got.Change.Author, got.Change.RoomID)
	}
	r := f.seed()
	if out, err := f.call("", "reply", r.ChangeID, "f1", "fixed", "--message", "Renamed"); err != nil || !strings.Contains(out, "Renamed") {
		t.Fatalf("root reply: %s %v", out, err)
	}
	if out, err := f.call("", "close", r.ChangeID); err != nil || !strings.Contains(out, `"state": "closed"`) {
		t.Fatalf("root close: %s %v", out, err)
	}
}

// Help must not initialize a service or start background work.
func TestHelpDoesNotStartService(t *testing.T) {
	storetest.Isolate(t)
	cmd := Command(Options{Service: func() *watch.Service {
		t.Fatal("help initialized a background service")
		return nil
	}})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestRoomCITimeBudgetPolicy(t *testing.T) {
	f := setup(t)
	for _, mode := range []string{"enforce", "warn"} {
		if _, err := f.call("", "policy", "--room", f.room, "--ci-time-budget", mode); err != nil {
			t.Fatal(err)
		}
		p, err := f.s.RoomChangePolicy(f.room)
		if err != nil || p.CITimeBudget != mode {
			t.Fatalf("policy=%+v err=%v", p, err)
		}
	}
	if _, err := f.call("", "policy", "--room", f.room, "--ci-time-budget", "off"); err == nil {
		t.Fatal("accepted third mode")
	}
}

func TestRoomGitHubAccessPolicyCommand(t *testing.T) {
	f := setup(t)
	for _, access := range []string{"read-only", "read-write"} {
		out, err := f.call("", "policy", "--room", f.room, "--github", access)
		if err != nil || !strings.Contains(out, `"githubAccess": "`+access+`"`) {
			t.Fatalf("policy %s: %s %v", access, out, err)
		}
		rec, err := f.s.Notify.Rooms.Resolve(f.room)
		if err != nil || rec.GitHubAccess != access {
			t.Fatalf("saved policy = %q %v", rec.GitHubAccess, err)
		}
	}
	if _, err := f.call("", "policy", "--room", f.room, "--github", "invalid"); err == nil {
		t.Fatal("accepted invalid access")
	}
	f.as(f.author.ID)
	if _, err := f.call("", "policy", "--room", f.room, "--github", "read-write"); err == nil {
		t.Fatal("agent changed room write access")
	}
}

func TestGitHubVerdictWhileWatcherPausedPrintsSavedNotice(t *testing.T) {
	f := setup(t)
	r := f.seed()
	r.Kind, r.Host, r.Repo, r.Number = "github", "github.com", "owner/repo", 17
	st := watch.State{Version: 1, Records: map[string]*watch.Record{r.Key: &r}, Reviewers: map[string]watch.Reviewer{}}
	r.QuotaAccount = "fixture"
	scope := watch.RateLimitScope{Host: r.Host, Account: r.QuotaAccount, Resource: "core"}
	st.RateLimits = map[string]watch.RateLimit{scope.Key(): {PauseUntil: time.Now().Add(time.Hour)}}
	f.write(st)
	f.as(f.reviewer.ID)
	out, err := f.call("", "verdict", r.ChangeID, "approve", "Reviewed the assigned head", "--head", r.Head)
	if err != nil || out != "verdict saved; applies after GitHub refresh\n" {
		t.Fatalf("paused verdict: output=%q, error=%v", out, err)
	}
	saved, err := f.s.Store.Get(r.Key)
	if err != nil || saved.PendingVerdict == nil || saved.Verdict != "" {
		t.Fatalf("pending verdict changed readiness or was lost: %+v, %v", saved, err)
	}
}

func TestNamedReviewerPolicyUsesExistingEngineCall(t *testing.T) {
	f := setup(t)
	f.root()
	if out, err := f.call("", "policy", "--room", f.room, "--reviewers", f.reviewer.ID); err != nil {
		t.Fatalf("set reviewers: %s %v", out, err)
	}
	policy, err := f.s.RoomChangePolicy(f.room)
	if err != nil || len(policy.Reviewers) != 1 || policy.Reviewers[0] != f.reviewer.ID {
		t.Fatalf("stored reviewers = %+v, %v", policy.Reviewers, err)
	}
	f.as(f.author.ID)
	if _, err = f.call("", "policy", "--room", f.room, "--reviewers", f.author.ID); err == nil {
		t.Fatal("an author named itself a reviewer")
	}
	f.root()
	if _, err = f.call("", "policy", "--room", f.room, "--reviewers", ""); err != nil {
		t.Fatal(err)
	}
	policy, err = f.s.RoomChangePolicy(f.room)
	if err != nil || len(policy.Reviewers) != 0 {
		t.Fatalf("roster was not cleared: %+v, %v", policy.Reviewers, err)
	}
}
