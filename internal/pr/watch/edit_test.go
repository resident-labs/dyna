package watch

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

// Editing a fork-head upstream PR runs gh pr edit, which the policy admits for
// the fork's parent. A raw PATCH to the upstream API is refused for fork heads.
func TestEditUpstreamPRUsesPREditCommand(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "fork-edit"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: &Store{Root: t.TempDir()}, Notify: notify.New(rooms, subagent.NewManager())}
	const key = "github.com/upstream/project#7"
	const url = "https://github.com/upstream/project/pull/7"
	if err := s.Store.update(func(st *State) error {
		st.Records = map[string]*Record{key: {Key: key, Host: "github.com", Repo: "upstream/project", HeadRepo: "fork/project", Number: 7, URL: url, RoomID: rm.ID, State: state.PROpen, Watching: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var agentText []bool
	s.GH = GitHub{Executable: "gh", run: func(ctx context.Context, cmd *exec.Cmd) error {
		calls = append(calls, cmd.Args[1:])
		agentText = append(agentText, ghclient.IsAgentText(ctx))
		return nil
	}}
	title := "Fix the parser"
	// An agent's edit is marked for the privacy checks; the owner's is not.
	for actor, want := range map[authority.Actor]bool{{Role: authority.Session, Session: "ses_a"}: true, {Role: authority.Owner}: false} {
		agentText = nil
		if err := s.Edit(authority.WithActor(context.Background(), actor), key, PREdit{Title: &title}); err != nil || len(agentText) != 1 || agentText[0] != want {
			t.Fatalf("%s edit: marked = %v, err = %v; want marked %v", actor.Role, agentText, err, want)
		}
	}
	calls = nil
	if err := s.Edit(context.Background(), key, PREdit{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("gh calls = %q, want one", calls)
	}
	got := strings.Join(calls[0], " ")
	if !strings.HasPrefix(got, "pr edit "+url+" --title ") {
		t.Fatalf("gh call = %q, want gh pr edit on the upstream URL", got)
	}
}
