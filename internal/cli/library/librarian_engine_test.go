package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/librarian"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestLibrarianMutationsReturnEngineFailure(t *testing.T) {
	data := storetest.Isolate(t)
	repo := authorityRepo(t)
	calls := map[string]websvc.Call{}
	for _, name := range []string{"EnableLibrarian", "DisableLibrarian", "WakeLibrarian", "FileLibrarySmell"} {
		calls[name] = func(context.Context, []json.RawMessage) (any, error) {
			return nil, errors.New("engine refused librarian change")
		}
	}
	enginetest.Serve(t, data, calls)
	for _, args := range [][]string{
		{"enable", "--room", "missing"},
		{"disable"},
		{"wake"},
		{"smell", "A smell", "--at", "README.md", "--symbols", "Run", "--cost", "Repeated work.", "--direction", "Share it."},
	} {
		c := Command(Options{DataDir: data})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(append(args, "--repo", repo))
		if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "engine refused librarian change") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestLibrarianCommandOutput(t *testing.T) {
	data := storetest.Isolate(t)
	repo := authorityRepo(t)
	checkRepo := func(got string) error {
		if got != repo {
			return errors.New("CLI did not send its resolved repository")
		}
		return nil
	}
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "EnableLibrarian", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(got, ref, cwd string) (librarian.Enabled, error) {
			if ref != "room" || !filepath.IsAbs(cwd) {
				return librarian.Enabled{}, errors.New("CLI lost the room or working directory")
			}
			return librarian.Enabled{Repo: repoid.Repo{Main: repo}, Room: "room"}, checkRepo(got)
		}},
		{Name: "DisableLibrarian", Access: websvc.Write, Scope: websvc.ScopeAdmin, Method: func(got string) (repoid.Repo, error) { return repoid.Repo{Main: repo}, checkRepo(got) }},
		{Name: "WakeLibrarian", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(got string) (librarian.Result, error) {
			return librarian.Result{Action: "current", Reason: "up to date"}, checkRepo(got)
		}},
		{Name: "FileLibrarySmell", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(got string, sm librarian.Smell) (room.Issue, error) {
			return room.Issue{ID: "issue", Number: 12, Title: sm.Title}, checkRepo(got)
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, data, calls)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"enable", "--room", "room"}, "librarian on for " + repo + " in room room; hook librarian-main runs dyna library wake\n"},
		{[]string{"disable"}, "librarian off for " + repo + "\n"},
		{[]string{"wake"}, "librarian current up to date\n"},
		{[]string{"wake", "--json"}, "{\"action\":\"current\",\"reason\":\"up to date\"}\n"},
		{[]string{"smell", "A smell"}, "filed D-12 : A smell\n"},
	} {
		var out bytes.Buffer
		c := Command(Options{DataDir: data})
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs(append(tc.args, "--repo", repo))
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != tc.want {
			t.Fatalf("%v: got %q, want %q", tc.args, got, tc.want)
		}
	}
}
