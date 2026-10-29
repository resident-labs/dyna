package watch

import (
	"context"
	"os/exec"
	"strings"
	"testing"

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
	s.GH = GitHub{Executable: "gh", run: func(_ context.Context, cmd *exec.Cmd) error {
		calls = append(calls, cmd.Args[1:])
		return nil
	}}
	title := "Fix the parser"
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
