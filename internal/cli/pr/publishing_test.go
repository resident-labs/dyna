package pr

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	prview "dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

func TestPublishingCLIBlocksAgentsAndKeepsMarkdownLocal(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "local-review"})
	if err != nil {
		t.Fatal(err)
	}
	const reviewer = "ses_20261017-000001-aaaaaaaa"
	const url = "https://github.com/owner/repo/pull/17"
	const key = "github.com/owner/repo#17"
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s := watch.New()
	s.Notify = notify.New(rooms, subagent.NewManager())
	// publishing is an engine call: the fake engine serves it from a pr/view
	// service over this same PR store.
	view := &prview.Service{Notify: s.Notify}
	s.Store = view.Watcher().Store
	engineCalls, err := websvc.Bind([]websvc.Binding{
		{Name: "RecordReviewResult", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.RecordReviewResult},
		{Name: "SetReviewPublishing", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: view.SetReviewPublishing},
		{Name: "CommentPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.CommentPR},
		{Name: "PostPRReview", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: view.PostPRReview},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), engineCalls)
	fake := filepath.Join(t.TempDir(), "gh")
	writes := filepath.Join(t.TempDir(), "writes")
	t.Setenv("PR_CLI_WRITES", writes)
	script := `#!/bin/sh
case "$*" in *--include*) printf 'HTTP/2.0 200 OK\r\n\r\n';; esac
case "$*" in
 *"graphql"*) printf '%s' '{"data":{"r0":{"defaultBranchRef":{"name":"main"},"p17":{"number":17,"state":"OPEN","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","mergeable":"MERGEABLE","isDraft":false}}}}';;
 *"--method POST"*) printf '%s\n' "$*" >> "$PR_CLI_WRITES"; printf '{"id": %s}' "${PR_CLI_ID:-987654}";;
 *"check-runs?"*) printf '{"check_runs":[{"name":"unit","status":"completed","conclusion":"success"}]}';;
 *"actions/runs?"*) printf '{"workflow_runs":[]}';;
 *"/statuses?"*) printf '[]';;
 *"/reviews?"*|*"/comments?"*) printf '[]';;
 *) printf '{"state":"open","head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}';;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.GH.Rooms = rooms
	roomfixture.Repository(t, rooms, rec.ID, "github.com", "owner/repo", "read-write")
	s.GH.Executable = fake
	t.Setenv(ghclient.BinaryEnv, fake)
	input := ""
	call := func(args ...string) (string, error) {
		c := Command(Options{Service: func() *watch.Service { return s }, Session: func(string) (subagent.Info, error) { return subagent.Info{Meta: subagent.Meta{ID: reviewer}}, nil }})
		var out bytes.Buffer
		c.SetIn(strings.NewReader(input))
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs(args)
		err := c.Execute()
		return out.String(), err
	}
	if out, err := call("publishing", "--room", rec.Name); err != nil || !strings.Contains(out, `"reviewPublishing": "local"`) {
		t.Fatalf("default: %s, %v", out, err)
	}
	for _, value := range []string{"github", "local"} {
		if out, err := call("publishing", value, "--room", rec.Name); err != nil || !strings.Contains(out, `"reviewPublishing": "`+value+`"`) {
			t.Fatalf("set: %s, %v", out, err)
		}
	}
	if _, err := call("publishing", "typo", "--room", rec.Name); err == nil {
		t.Fatal("invalid publishing accepted")
	}
	st := watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url, RoomID: rec.ID, Author: "", Reviewer: reviewer, ReviewerMode: "session", Head: head, RoundHead: head, Round: 1, State: "open", Watching: true}}, Reviewers: map[string]watch.Reviewer{}}
	raw, _ := json.Marshal(st)
	saveState(t, s.Store.Root, raw)
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, reviewer)
	t.Setenv(mode.EnvRoomID, rec.ID)
	if _, err := call("comment", url, "accidental post"); err == nil || !strings.Contains(err.Error(), "agents cannot publish") {
		t.Fatalf("local comment error = %v", err)
	}
	if _, err := os.Stat(writes); !os.IsNotExist(err) {
		t.Fatalf("local comment wrote GitHub: %v", err)
	}
	feedback := "## Findings\n\n`parser.go:42`: bound check.\n\n`tail.go:99`: final item.\n"
	file := filepath.Join(t.TempDir(), "review.md")
	if err := os.WriteFile(file, []byte(feedback), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := call("verdict", url, "changes", "--body-file", file, "--head", head); err != nil {
		t.Fatal(err)
	}
	input = feedback
	if _, err := call("verdict", url, "changes", "--body-file", "-", "--head", head); err != nil {
		t.Fatalf("stdin review: %v", err)
	}
	got, _ := s.Store.Get(key)
	messages, err := rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	feedbackPosts := 0
	for _, message := range messages {
		if strings.Contains(message.Text, strings.TrimSpace(feedback)) {
			feedbackPosts++
		}
	}
	if got.Summary != feedback || feedbackPosts != 1 {
		t.Fatalf("file feedback lost: %+v / %+v", got, messages)
	}

	if _, err := os.Stat(writes); !os.IsNotExist(err) {
		t.Fatalf("local verdict wrote GitHub: %v", err)
	}

	for _, agentMode := range []string{mode.Subagent, mode.Root} {
		t.Setenv(mode.EnvMode, agentMode)
		for _, args := range [][]string{
			{"review", url, "--body-file", file, "--head", head, "--publish", "--comments-file", file},
			{"publishing", "github", "--room", rec.Name},
		} {
			if _, err := call(args...); err == nil {
				t.Fatalf("%s accepted %v", agentMode, args)
			}
		}
	}
	if _, err := os.Stat(writes); !os.IsNotExist(err) {
		t.Fatalf("agent wrote GitHub: %v", err)
	}
	t.Setenv(mode.EnvMode, mode.Subagent)
	stale := strings.Repeat("b", 40)
	if _, err := call("review", url, "--body-file", file, "--head", stale, "--publish"); err == nil || !strings.Contains(err.Error(), "tracked head") {
		t.Fatalf("stale agent review head: %v", err)
	}
	reviewFile := file
	// The owner can still publish comments and reviews without attribution.
	storetest.ClearStamp(t)
	t.Setenv("PR_CLI_ID", "111")
	if _, err := call("comment", url, "owner public post", "--publish"); err != nil {
		t.Fatalf("owner comment: %v", err)
	}
	if _, err := call("review", url, "--body-file", reviewFile, "--head", head, "--publish"); err != nil {
		t.Fatalf("owner review: %v", err)
	}
	written, _ := os.ReadFile(writes)
	if !strings.Contains(string(written), "owner public post") {
		t.Fatalf("owner comment was not posted: %s", written)
	}
	st, err = s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"issue-comments:111", "reviews:111"} {
		if owner, ok := st.CommentSessions[key]; ok {
			t.Fatalf("owner post %s attributed to %q", key, owner)
		}
	}
}
