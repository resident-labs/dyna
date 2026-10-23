package issue

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

type issueFixture struct {
	rooms *room.Store
	rec   room.Record
	svc   *roomsvc.Service
}

func newIssueFixture(t *testing.T) issueFixture {
	t.Helper()
	root := storetest.Isolate(t)
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	svc := roomsvc.New(notify.New(rooms, &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}, Rooms: rooms}))
	return issueFixture{rooms, rec, svc}
}
func (f issueFixture) run(args ...string) (string, error) {
	c := Command(Options{Service: func() *roomsvc.Service { return f.svc }})
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SilenceUsage = true
	c.SetArgs(append([]string{"--room", f.rec.ID}, args...))
	err := c.Execute()
	return out.String(), err
}
func (f issueFixture) file(t *testing.T) room.Issue {
	t.Helper()
	i, err := f.rooms.FileIssue(f.rec.ID, "Fix parser", "details", "parser.go", 12, f.rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func (f issueFixture) fakeGitHub(t *testing.T) {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/test/issues.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	if err := f.rooms.UpdatePreferences(f.rec.ID, func(r *room.Record) error { r.Repo = repo; r.GitHubAccess = ghclient.ReadWrite; return nil }); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"login":"writer"}]}}';;
"auth token") echo fixture-token;;
"api --hostname") echo '{"permissions":{"push":true}}';;
"issue create") echo https://github.com/test/issues/issues/42;;
*) exit 1;;
esac
`
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, exe)
}

func TestIssueCommandsUseReadableOutputAndExplicitJSON(t *testing.T) {
	for _, action := range []string{"file", "list", "close", "goal", "export"} {
		for _, asJSON := range []bool{false, true} {
			name := action + "/human"
			if asJSON {
				name = action + "/json"
			}
			t.Run(name, func(t *testing.T) {
				f := newIssueFixture(t)
				args := []string{"Fix parser", "--at", "parser.go:12", "--note", "details"}
				if action != "file" {
					f.file(t)
					args = []string{action, "D-1"}
					switch action {
					case "list":
						args = []string{"list"}
					case "close":
						args = append(args, "--reason", "done")
					case "export":
						f.fakeGitHub(t)
					}
				}
				if asJSON {
					args = append(args, "--json")
				}
				out, err := f.run(args...)
				if err != nil {
					t.Fatalf("command: %v; %s", err, out)
				}
				rows, err := f.rooms.Issues(f.rec.ID, true)
				if err != nil || len(rows) != 1 {
					t.Fatal(rows, err)
				}
				i := rows[0]
				if asJSON {
					var got room.Issue
					if action == "list" {
						var list []room.Issue
						if err = json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 {
							t.Fatal(out, err)
						}
						got = list[0]
					} else {
						if err = json.Unmarshal([]byte(out), &got); err != nil {
							t.Fatal(out, err)
						}
					}
					if got != i {
						t.Fatalf("JSON issue = %+v, want %+v", got, i)
					}
					return
				}
				want := ""
				switch action {
				case "file":
					want = "filed D-1 : " + i.Title + "\n"
				case "list":
					want = "D-1  open  " + i.Title + "  parser.go:12\n"
				case "close":
					want = "closed D-1 : " + i.Title + "\n"
				case "goal":
					want = "goal D-1 : " + i.Title + " -> " + i.GoalID + "\n"
				case "export":
					want = "exported D-1 : " + i.Title + " -> " + i.GitHubURL + "\n"
				}
				if out != want {
					t.Fatalf("human output = %q, want %q", out, want)
				}
			})
		}
	}
}

func TestIssueCloseAcceptsPositionalReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"positional", []string{"fixed elsewhere"}, false},
		{"flag", []string{"--reason", "fixed elsewhere"}, false},
		{"matching", []string{"fixed elsewhere", "--reason", "fixed elsewhere"}, false},
		{"different", []string{"another reason", "--reason", "fixed elsewhere"}, true},
		{"missing", nil, true},
		{"empty", []string{" "}, true},
		{"extra", []string{"fixed elsewhere", "extra"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newIssueFixture(t)
			i := f.file(t)
			out, err := f.run(append([]string{"close", i.ID}, tc.args...)...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("close: %v; %s", err, out)
			}
			rows, e := f.rooms.Issues(f.rec.ID, true)
			if e != nil {
				t.Fatal(e)
			}
			if tc.wantErr {
				if rows[0].State != "open" {
					t.Fatal("rejected close changed issue")
				}
				if tc.name == "different" && !strings.Contains(err.Error(), "disagree") {
					t.Fatalf("unclear conflict: %v", err)
				}
			} else if rows[0].State != "closed" || rows[0].Reason != "fixed elsewhere" {
				t.Fatalf("closed issue: %+v", rows[0])
			}
		})
	}
}
