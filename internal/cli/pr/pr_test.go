package pr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestPRCommandsAndPauseExplanation(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
	// The room's repository: the engine tracks only from a checkout of it.
	checkout, _ := filepath.EvalSymlinks(t.TempDir())
	rec, err := rooms.Create(room.CreateInput{Name: "work", Repo: checkout})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, rooms, rec.ID, "github.com", "owner/repo", "read-write")
	// The owner's CLI acts in the room of its working directory.
	t.Chdir(checkout)
	author := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000001-aaaaaaaa", RoomID: rec.ID, Room: rec.Name, Dir: t.TempDir()}}
	reviewer := author
	reviewer.ID = "ses_20261017-000002-bbbbbbbb"
	mgr := subagent.NewManager()
	mgr.Profiles = func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{{Name: "judge", Harness: "mock"}}}, nil
	}
	s := watch.New()
	s.Notify = notify.New(rooms, mgr)
	// track, untrack, resume and the admin commands are engine calls: the
	// fake engine serves them from a pr/view service over this same PR store.
	view := &prview.Service{Notify: s.Notify,
		SessionInRoom: func(_, ref string) (subagent.Info, error) {
			return mgr.Store.ResolveSnapshot(ref)
		},
		WorkspaceRoot: func(string, string) (string, error) { return checkout, nil },
	}
	s.Store = view.Watcher().Store
	for _, meta := range []subagent.Meta{author.Meta, reviewer.Meta} {
		recordtest.Write(t, filepath.Join(filepath.Dir(s.Store.Root), "sessions"), meta.ID, meta)
	}
	engineCalls, err := websvc.Bind([]websvc.Binding{
		{Name: "ReviewerStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: view.ReviewerStatus},
		{Name: "TrackPRRef", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.TrackPRRef},
		{Name: "SetReviewerProfile", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.SetReviewerProfile},
		{Name: "SetRoomChangePolicy", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.SetRoomChangePolicy},
		{Name: "SetReviewEnabled", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.SetReviewEnabled},
		{Name: "LearnPRNoise", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.LearnPRNoise},
		{Name: "RemovePRNoiseRule", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.RemovePRNoiseRule},
		{Name: "UntrackPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.UntrackPR},
		{Name: "ResumePR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.ResumePR},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), engineCalls)
	s.GH.Executable = "/must-not-contact-github"
	dir, _ := os.Getwd()
	const key = "github.com/owner/repo#17"
	const url = "https://github.com/owner/repo/pull/17"
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, Author: author.ID, RoomID: rec.ID, Dir: author.Dir, LookupDir: dir, State: "open", Watching: true, CIPaused: true, Failures: 3, PauseReason: "CI wakes paused after 3 consecutive failures"}}, Reviewers: map[string]watch.Reviewer{}}
	b, _ := json.Marshal(st)
	saveState(t, s.Store.Root, b)

	o := Options{Service: func() *watch.Service { return s }, Session: func(ref string) (subagent.Info, error) {
		if ref == reviewer.ID {
			return reviewer, nil
		}
		return author, nil
	}}
	call := func(args ...string) (string, error) {
		cmd := Command(o)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	if out, err := call("--help"); err != nil || !strings.Contains(out, "track") || !strings.Contains(out, "verdict") || strings.Contains(out, "  watch ") {
		t.Fatalf("help: %s %v", out, err)
	}

	if out, err := call("budget", "--room", rec.Name); err != nil || !strings.Contains(out, `"warnAfter": "2m0s"`) {
		t.Fatalf("budget defaults: %s %v", out, err)
	}
	if out, err := call("budget", "--room", rec.Name, "--warn", "30s", "--fail", "1m"); err != nil || !strings.Contains(out, `"failAfter": "1m0s"`) {
		t.Fatalf("budget update: %s %v", out, err)
	}
	if out, err := call("budget", "--room", rec.Name, "--exempt", "CodeQL"); err != nil || !strings.Contains(out, `"CodeQL"`) {
		t.Fatalf("budget exemptions: %s %v", out, err)
	}
	if out, err := call("budget", "--room", rec.Name); err != nil || !strings.Contains(out, `"CodeQL"`) {
		t.Fatalf("stored exemptions: %s %v", out, err)
	}
	if out, err := call("budget", "--room", rec.Name, "--exempt", ""); err != nil || !strings.Contains(out, `"ciBudgetExempt": []`) {
		t.Fatalf("clear exemptions: %s %v", out, err)
	}
	if _, err := call("budget", "--room", rec.Name, "--warn", "2m", "--fail", "1m"); err == nil {
		t.Fatal("invalid budget accepted")
	}
	if out, err := call("budget", "--room", rec.Name, "--test", "2s", "--allow", "pkg/Slow", "--no-junit-warning=false"); err != nil || !strings.Contains(out, `"testMax": "2s"`) || !strings.Contains(out, `"pkg/Slow"`) || !strings.Contains(out, `"noJUnitWarning": false`) {
		t.Fatalf("test budget update: %s %v", out, err)
	}
	if _, err := call("budget", "--room", rec.Name, "--test", "0s"); err == nil {
		t.Fatal("invalid test budget accepted")
	}
	if out, err := call("budget", "--room", rec.Name, "--allow", ""); err != nil || strings.Contains(out, `"pkg/Slow"`) {
		t.Fatalf("clear allowlist: %s %v", out, err)
	}
	if out, err := call("verdict", "--help"); err != nil || !strings.Contains(out, "--head") || !strings.Contains(out, "--review-id") {
		t.Fatalf("verdict help: %s %v", out, err)
	}
	if out, err := call("track", url); err != nil || !strings.Contains(out, `"created": false`) {
		t.Fatalf("idempotent track: %s %v", out, err)
	}
	if out, err := call("track", url, "--repo", checkout); err != nil || !strings.Contains(out, checkout) {
		t.Fatalf("track explicit checkout: %s %v", out, err)
	}
	saved, err := s.Store.Get(key)
	if err != nil || saved.RepoPath != checkout {
		t.Fatalf("saved checkout: %+v %v", saved, err)
	}
	if _, err := call("track", url, "--repo", t.TempDir()); err == nil {
		t.Fatal("accepted invalid explicit checkout")
	}
	if out, err := call("show", url); err != nil || !strings.Contains(out, "CI wakes paused after 3 consecutive failures") {
		t.Fatalf("show pause: %s %v", out, err)
	}
	if _, err = call("reviewer", "--room", rec.Name, "--session", reviewer.ID); err == nil {
		t.Fatal("removed --session accepted")
	}
	if _, err = call("reviewer", "--room", rec.Name, "--profile", "judge"); err != nil {
		t.Fatal(err)
	}
	if out, err := call("reviewer", "--room", rec.Name); err != nil || !strings.Contains(out, `"profile": "judge"`) {
		t.Fatalf("reviewer profile: %s %v", out, err)
	}
	if _, err = call("reviewer", "--room", rec.Name, "--profile", "judge", "--session", reviewer.ID); err == nil {
		t.Fatal("ambiguous reviewer accepted")
	}
	if _, err = call("review", "off", "--room", rec.Name); err != nil {
		t.Fatal(err)
	}
	fakeGH := filepath.Join(t.TempDir(), "gh")
	const script = `#!/bin/sh
case "$*" in *--include*) printf 'HTTP/2.0 200 OK\r\n\r\n';; esac
case "$*" in
  */issues/*/comments*) printf '%s\n' '[{"id":101,"body":"Quota unavailable for @alice. Retry after 30 minutes.","user":{"login":"quota-bot[bot]","type":"Bot"},"html_url":"https://github.com/owner/repo/pull/17#issuecomment-101"}]' ;;
  */comments*|*/reviews*) printf '%s\n' '[]' ;;
  *) printf '%s\n' '{"user":{"login":"owner"},"state":"open"}' ;;
esac
`
	if err = os.WriteFile(fakeGH, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.GH.Rooms = rooms
	s.GH.Executable = fakeGH
	// The engine's own watcher, which serves untrack and resume, uses it too.
	t.Setenv(ghclient.BinaryEnv, fakeGH)
	noiseOut, err := call("noise", url, "issue-comments:101", "--reason", "quota notification")
	if err != nil {
		t.Fatalf("learn noise: %s %v", noiseOut, err)
	}
	var learned struct {
		Rule    watch.NoiseRule `json:"rule"`
		Created bool            `json:"created"`
	}
	if err = json.Unmarshal([]byte(noiseOut), &learned); err != nil || !learned.Created || learned.Rule.ID == "" {
		t.Fatalf("noise result: %s %v", noiseOut, err)
	}
	if out, err := call("noise", url, "101"); err != nil || !strings.Contains(out, `"created": false`) {
		t.Fatalf("idempotent noise: %s %v", out, err)
	}
	if out, err := call("noise", "list"); err != nil || !strings.Contains(out, learned.Rule.ID) {
		t.Fatalf("noise list: %s %v", out, err)
	}
	if out, err := call("show", url); err != nil || !strings.Contains(out, `"noiseRules"`) || !strings.Contains(out, learned.Rule.ID) {
		t.Fatalf("show learned noise: %s %v", out, err)
	}
	if _, err = call("noise", "rm", learned.Rule.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := call("noise", "list"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("noise remove: %s %v", out, err)
	}
	if _, err = call("untrack", url); err != nil {
		t.Fatal(err)
	}
	if out, err := call("track", url); err != nil || !strings.Contains(out, `"watching": false`) {
		t.Fatalf("track reactivated watch: %s %v", out, err)
	}
	if _, err = call("resume", url); err != nil {
		t.Fatal(err)
	}
	r, err := s.Store.Get(key)
	if err != nil || r.CIPaused || r.Failures != 0 || !r.Watching {
		t.Fatalf("resume: %+v %v", r, err)
	}
}

func TestShowNotificationHistory(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := watch.New()
	s.Store = &watch.Store{Root: t.TempDir()}
	const key = "github.com/owner/repo#17"
	const url = "https://github.com/owner/repo/pull/17"
	deliveredBody := strings.Repeat("Delivered notice body. ", 1024)
	pendingBody := strings.Repeat("Pending notice body. ", 1024)
	rec := &watch.Record{Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, Head: "current-head", CI: "passed", CIPaused: true, PauseReason: "CI wake pause", Notices: []watch.Notice{
		{ID: "done-1", Text: deliveredBody, Done: true},
		{ID: "pending", Text: pendingBody, Error: "delivery failed"},
		{ID: "done-2", Text: "Another delivered body", Done: true},
	}}
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: rec}}
	original, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, s.Store.Root, original)
	revision := storeRevision(t, s.Store.Root)
	for _, history := range []bool{false, true} {
		for _, jsonFlag := range []bool{false, true} {
			cmd := Command(Options{Service: func() *watch.Service { return s }})
			args := []string{"show", url}
			if history {
				args = append(args, "--history")
			}
			if jsonFlag {
				args = append(args, "--json")
			}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var got struct {
				watch.Record
				DeliveredNotifications int `json:"deliveredNotifications"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			wantNotices := 1
			if history {
				wantNotices = 3
			}
			if got.Head != rec.Head || got.CI != rec.CI || got.PauseReason != rec.PauseReason || !got.CIPaused || got.DeliveredNotifications != 2 || len(got.Notices) != wantNotices {
				t.Fatalf("history=%t json=%t: unexpected status or notices: %+v", history, jsonFlag, got)
			}
			if strings.Contains(out.String(), deliveredBody) != history || !strings.Contains(out.String(), pendingBody) || !strings.Contains(out.String(), "delivery failed") {
				t.Fatalf("history=%t json=%t: notice bodies did not match flags", history, jsonFlag)
			}
		}
	}
	if storeRevision(t, s.Store.Root) != revision {
		t.Fatal("show changed stored notices")
	}
	cmd := Command(Options{})
	var help bytes.Buffer
	cmd.SetOut(&help)
	cmd.SetArgs([]string{"show", "--help"})
	if err := cmd.Execute(); err != nil || !strings.Contains(help.String(), "--history") || !strings.Contains(help.String(), "--json") {
		t.Fatalf("show help: %s %v", help.String(), err)
	}
}

func TestReviewerPoolHelp(t *testing.T) {
	storetest.Isolate(t)
	for _, args := range [][]string{{"--help"}, {"reviewer", "--help"}} {
		cmd := Command(Options{})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "reviewer pool") || strings.Contains(out.String(), "one at a time") {
			t.Fatalf("help does not describe the pool: %s", out.String())
		}
	}
}

func TestReviewerCommandReturnsEveryPoolSlot(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rooms := &room.Store{Root: filepath.Join(t.TempDir(), "rooms")}
	r, err := rooms.Create(room.CreateInput{Name: "pool"})
	if err != nil {
		t.Fatal(err)
	}
	s := watch.New()
	s.Store = &watch.Store{Root: t.TempDir()}
	s.Notify = notify.New(rooms, subagent.NewManager())
	s.ReviewerProfiles = func() ([]profile.Profile, error) {
		return []profile.Profile{{Name: "judge", Harness: "mock"}}, nil
	}
	s.ReviewerInfo = func(id string) (subagent.Info, subagent.Turn, error) {
		return subagent.Info{Meta: subagent.Meta{ID: id, Name: id, Room: r.Name, RoomID: r.ID, Role: "reviewer", Profile: "judge", Turns: 1}, Status: subagent.StatusRunning, CurrentTurn: 1}, subagent.Turn{N: 1, Status: subagent.TurnRunning}, nil
	}
	const first = "github.com/owner/repo#12"
	const second = "github.com/owner/repo#13"
	cfg := watch.Reviewer{Profile: "judge"}
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		first:  {Key: first, Number: 12, Head: "head-one", RoundHead: "head-one", Reviewer: "s1", RoomID: r.ID, State: "open", Watching: true},
		second: {Key: second, Number: 13, Head: "head-two", RoundHead: "head-two", Reviewer: "s2", RoomID: r.ID, State: "open", Watching: true},
	}, Reviewers: map[string]watch.Reviewer{r.ID: cfg}}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, s.Store.Root, data)
	calls, err := websvc.Bind([]websvc.Binding{{Name: "ReviewerStatus", Access: websvc.Read, Scope: websvc.ScopeRead, Method: s.ReviewerStatus}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	cmd := Command(Options{Service: func() *watch.Service { return s }})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"reviewer", "--room", r.Name})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var status watch.ReviewerStatus
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("status JSON: %s: %v", out.String(), err)
	}
	if len(status.Reviewers) != 2 {
		t.Fatalf("pool entries: %s", out.String())
	}
	for i, slot := range status.Reviewers {
		if slot.Session == nil || slot.Session.ID != fmt.Sprintf("s%d", i+1) || slot.Profile != "judge" {
			t.Fatalf("slot %d: %+v", i, slot)
		}
	}
	if status.Reviewers[0].Current == nil || status.Reviewers[1].Current == nil || status.Reviewers[0].Current.PR != 12 || status.Reviewers[1].Current.PR != 13 || status.Reviewers[1].Status != "reviewing" {
		t.Fatalf("pool status: %s", out.String())
	}
	if status.Session == nil || status.Session.ID != "s1" || status.Current == nil || status.Current.PR != 12 {
		t.Fatalf("legacy first slot: %s", out.String())
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

func TestDaemonCommandRemoved(t *testing.T) {
	storetest.Isolate(t)
	for _, cmd := range NewCommand().Commands() {
		if cmd.Name() == "watch" {
			t.Fatalf("daemon command %q remains registered", cmd.Name())
		}
	}
}
