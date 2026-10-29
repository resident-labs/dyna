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

func TestEditPRCommandUsesEngineAndAuthorRules(t *testing.T) {
	storetest.Isolate(t)
	rooms := room.DefaultStore()
	rec, err := rooms.Create(room.CreateInput{Name: "edit-pr"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, rooms, rec.ID, "github.com", "owner/repo", "read-write")
	svc := &prview.Service{Notify: notify.New(rooms, subagent.NewManager())}
	s := svc.Watcher()
	const (
		author    = "ses_20261029-000001-aaaaaaaa"
		unrelated = "ses_20261029-000002-bbbbbbbb"
		url       = "https://github.com/owner/repo/pull/17"
		key       = "github.com/owner/repo#17"
	)
	state := watch.State{Version: 1, Records: map[string]*watch.Record{key: {
		Key: key, Host: "github.com", Repo: "owner/repo", Number: 17, URL: url,
		RoomID: rec.ID, Author: author, State: "open", Watching: true,
	}}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, s.Store.Root, raw)
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "EditPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: svc.EditPR},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)

	writes := filepath.Join(t.TempDir(), "writes")
	t.Setenv("PR_EDIT_WRITES", writes)
	fake := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$*" in *--include*) printf 'HTTP/2.0 200 OK\r\n\r\n';; esac
case "$*" in
 "pr edit "*) printf '%s\n' "$@" >> "$PR_EDIT_WRITES"; while [ $# -gt 0 ]; do [ "$1" = --body-file ] && cat "$2" >> "$PR_EDIT_WRITES"; shift; done;;
 *) printf '{"user":{"login":"owner"},"state":"open","head":{"sha":"head"}}';;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, fake)
	body := "    Literal `dyna node list`.\nCredential: ghp_1234567890abcdef\n"
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvRoomID, rec.ID)
	call := func(session string, args ...string) error {
		t.Setenv(mode.EnvSession, session)
		cmd := Command(Options{Service: func() *watch.Service { return s }})
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := call(author, "edit", url); err == nil || !strings.Contains(err.Error(), "--title or --body-file") {
		t.Fatalf("edit without a change = %v", err)
	}
	if err := call(author, "edit", url, "--title", "Edited title", "--body-file", bodyFile); err != nil {
		t.Fatalf("author edit = %v", err)
	}
	written, err := os.ReadFile(writes)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pr\nedit\n" + url + "\n--title\nEdited title\n--body-file\n",
		"    Literal `dyna node list`.\nCredential: [redacted]\n",
	} {
		if !strings.Contains(string(written), want) {
			t.Errorf("GitHub write misses %q:\n%s", want, written)
		}
	}
	if strings.Contains(string(written), "ghp_1234567890abcdef") {
		t.Fatalf("GitHub write exposed a token: %s", written)
	}

	before := string(written)
	if err := call(unrelated, "edit", url, "--title", "Stolen title"); err == nil || !strings.Contains(err.Error(), "git pr edit") {
		t.Fatalf("unrelated session edit = %v", err)
	}
	after, err := os.ReadFile(writes)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("unrelated session wrote to GitHub:\n%s", after)
	}
}
