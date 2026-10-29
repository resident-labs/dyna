package watch

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestCloseCleansUpActiveCI(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.Ref = "feature"
	e.write()
	r := e.track()
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	e.f.Runs = []Run{{ID: 100, HeadBranch: "feature", Event: "pull_request", Status: "queued"}}
	e.f.Raw["repos/owner/repo/actions/runs/100/cancel"] = "{}"
	e.write()

	if err := e.s.Close(t.Context(), r.Key, ""); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if got.Watching || got.State != "closed" || !got.CICleanupDone {
		t.Fatalf("closed record: %+v", got)
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "pr close -- "+r.URL) || !strings.Contains(string(calls), "/runs/100/cancel") {
		t.Fatalf("close and cleanup calls: %s", calls)
	}
}

func TestCloseRecordsReasonBeforeWatcherDelivery(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	run := e.s.GH.run
	entered, release := make(chan struct{}), make(chan struct{})
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if len(cmd.Args) > 2 && cmd.Args[1] == "pr" && cmd.Args[2] == "close" {
			close(entered)
			<-release
		}
		return run(ctx, cmd)
	}

	closed := make(chan error, 1)
	go func() { closed <- e.s.Close(t.Context(), r.Key, "superseded by #18") }()
	<-entered
	pull := e.f.Pull
	pull.State = "closed"
	if err := e.s.process(t.Context(), r.Key, Snapshot{Pull: pull}, "", "", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := e.s.flush(t.Context(), r.Key); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}

	var outcomes []Notice
	for _, n := range e.sent {
		if n.Type == room.PRClosed {
			outcomes = append(outcomes, n)
		}
	}
	if len(outcomes) != 1 || !strings.Contains(outcomes[0].Text, "Reason: superseded by #18") {
		t.Fatalf("closed outcomes: %+v", outcomes)
	}
}
