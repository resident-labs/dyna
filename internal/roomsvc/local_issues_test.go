package roomsvc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/room"
)

func TestIssueGoalAndClose(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	i, err := e.svc.rooms().FileIssue(r.ID, room.IssueInput{Title: "Fix parser", Note: "one path", File: "parser.go", Line: 7, Filer: r.Name+":agent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.TriageIssue(context.Background(), r.ID, i.ID, "goal", "", r.Name+":agent"); err == nil {
		t.Fatal("agent triaged")
	}
	got, err := e.svc.IssueGoal(t.Context(), r.ID, i.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	g, err := e.svc.goalService().Store.Get(r.ID, got.GoalID)
	if err != nil || g.Text != i.Title || !strings.Contains(g.Detail, "Local issue D-1\n") || strings.Contains(g.Detail, i.ID) || !strings.Contains(g.Detail, "parser.go:7") || !strings.Contains(g.Detail, "one path") {
		t.Fatalf("goal: %+v %v", g, err)
	}
	if _, err = e.svc.CloseIssue(t.Context(), r.ID, i.ID, "done"); err == nil {
		t.Fatal("closed a goal issue")
	}
	rows, err := e.svc.Issues(r.ID, false)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestIssueExportAccountAndReadOnly(t *testing.T) {
	for _, tc := range []struct{ name, title string }{
		{"plain", "Export title"},
		{"foreign-url", "https://github.com/other/project/issues/1"},
		{"repository-prefix", "-Rother/project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			r := e.room("work")
			repo := t.TempDir()
			for _, a := range [][]string{{"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/test/issues.git"}} {
				if b, err := exec.Command("git", a...).CombinedOutput(); err != nil {
					t.Fatalf("git: %s %v", b, err)
				}
			}
			if err := e.svc.rooms().UpdatePreferences(r.ID, func(r *room.Record) error { r.Repo = repo; r.GitHubAccess = ghclient.ReadOnly; return nil }); err != nil {
				t.Fatal(err)
			}
			i, err := e.svc.rooms().FileIssue(r.ID, room.IssueInput{Title: tc.title, Note: "Export note", File: "a.go", Line: 3, Filer: r.Name})
			if err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(t.TempDir(), "gh")
			script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo selected-account-token;;
"api --hostname") [ "$4" = repos/test/issues ] || exit 52; echo '{"id":1,"permissions":{"push":true}}';;
"issue create")
 [ "$GH_TOKEN" = selected-account-token ] || exit 50
 printf '%s\n' "$@" > "$ISSUE_ARGS"
 echo https://github.com/test/issues/issues/42;;
*) exit 51;;
esac
`
			if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv(ghclient.BinaryEnv, exe)
			argsFile := filepath.Join(t.TempDir(), "args")
			t.Setenv("ISSUE_ARGS", argsFile)
			if _, err = e.svc.ExportIssue(context.Background(), r.ID, i.ID); err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("read-only export: %v", err)
			}
			if _, err = os.Stat(argsFile); !os.IsNotExist(err) {
				t.Fatal("read-only export invoked gh create")
			}
			if err := ghclient.SetAccess(e.svc.rooms(), r.ID, ghclient.ReadWrite); err != nil {
				t.Fatal(err)
			}
			got, err := e.svc.ExportIssue(context.Background(), r.ID, i.ID)
			if err != nil || got.State != "exported" || got.GitHubURL != "https://github.com/test/issues/issues/42" {
				t.Fatalf("export: %+v %v", got, err)
			}
			b, err := os.ReadFile(argsFile)
			if err != nil || !strings.Contains(string(b), tc.title) || !strings.Contains(string(b), "Local issue D-1\n") || strings.Contains(string(b), i.ID) || !strings.Contains(string(b), "a.go:3") || !strings.Contains(string(b), "--repo\ngithub.com/test/issues\n") {
				t.Fatalf("args: %s %v", b, err)
			}
		})
	}
}

func TestIssueEvents(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	offsets, members, signatures := map[string]int64{}, newMemberWatch(), map[string]string{}
	sig := ""
	e.svc.scan(offsets, &sig, members, signatures, true, func(Event) {})
	i, err := e.svc.rooms().FileIssue(r.ID, room.IssueInput{Title: "Watch me", Note: "", File: "", Line: 0, Filer: r.Name})
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	e.svc.scan(offsets, &sig, members, signatures, false, func(e Event) { events = append(events, e) })
	hasIssue := func() bool {
		for _, e := range events {
			if e.Kind == "issues" && e.RoomID == r.ID {
				return true
			}
		}
		return false
	}
	if !hasIssue() {
		t.Fatalf("no filing event: %+v", events)
	}
	if _, err = e.svc.CloseIssue(t.Context(), r.ID, i.ID, "done"); err != nil {
		t.Fatal(err)
	}
	events = nil
	e.svc.scan(offsets, &sig, members, signatures, false, func(e Event) { events = append(events, e) })
	if !hasIssue() {
		t.Fatalf("no triage event: %+v", events)
	}
}
