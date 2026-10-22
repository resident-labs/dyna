package hooks

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/processguard"
)

func TestHookRunLogBoundedTail(t *testing.T) {
	s, repo := setup(t)
	hookLaunchTestEngine(t, s.DataDir)
	h := Hook{Name: "tail", Event: AfterLand, Command: "exit 3", Cwd: "main", Timeout: "1m", OnFail: "alert", AddedBy: "test", AddedAt: time.Now()}
	if err := s.Add(context.Background(), repo, h); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(authority.InEngine())
	ev, err := s.Run(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), Event{Repo: repo}, h.Name)
	if err == nil || ev.Result != "fail" {
		t.Fatalf("RunHook = %+v, %v", ev, err)
	}
	const max = 8 << 10
	for _, tt := range []struct {
		name     string
		size     int64
		content  string
		exitCode int
	}{
		{name: "empty", exitCode: -1},
		{name: "short", content: "hello\n", exitCode: -1},
		{name: "exact limit", content: strings.Repeat("x", max), exitCode: -1},
		{name: "over limit", content: strings.Repeat("x", max) + "\nexit status 3\n", exitCode: 3},
		{name: "trailing whitespace", content: "broken\nexit status 42\n \t", exitCode: 42},
		{name: "large sparse log", size: 32 << 20, content: strings.Repeat("x", max-15) + "\nexit status 3\n", exitCode: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			size := tt.size
			if size == 0 {
				size = int64(len(tt.content))
			}
			f, err := os.Create(ev.LogPath)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := f.Truncate(size); err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteAt([]byte(tt.content), size-int64(len(tt.content))); err != nil {
				t.Fatal(err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			log, err := s.HookRunLog(repo, h.Name, ev.ID)
			runtime.ReadMemStats(&after)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.content
			if len(want) > max {
				want = want[len(want)-max:]
			}
			if tt.size > 0 && len(want) < max {
				want = strings.Repeat("\x00", max-len(want)) + want
			}
			if log.Tail != want || log.ExitCode != tt.exitCode {
				t.Fatalf("tail length = %d, want %d; exit code = %d, want %d", len(log.Tail), len(want), log.ExitCode, tt.exitCode)
			}
			// Allow metadata work, but never an allocation proportional to the log size.
			if tt.size > 0 && after.TotalAlloc-before.TotalAlloc > 4<<20 {
				t.Fatalf("reading an 8 KiB tail allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
			}
		})
	}
	if err := os.Remove(ev.LogPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HookRunLog(repo, h.Name, ev.ID); !os.IsNotExist(err) {
		t.Fatalf("missing log error = %v", err)
	}
}

func TestHookRunLogPreservesExecutionFailure(t *testing.T) {
	for _, tc := range []struct {
		name, command, want string
		cause               error
		exit                int
	}{
		{name: "exit", command: "exit 3", want: "exit status 3", exit: 3},
		{name: "timeout", command: "true", cause: context.DeadlineExceeded, want: "context deadline exceeded", exit: -1},
		{name: "launch", command: "true", cause: errors.New("start command: missing executable"), want: "start command: missing executable", exit: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, repo := setup(t)
			if tc.cause != nil {
				s.Exec = func(context.Context, processguard.Process) error { return tc.cause }
			} else {
				hookLaunchTestEngine(t, s.DataDir)
			}
			h := hook(tc.name, AfterLand)
			h.Command = tc.command
			add(t, s, repo, h)
			t.Cleanup(authority.InEngine())
			run, err := s.Run(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), Event{Repo: repo}, h.Name)
			if err == nil {
				t.Fatal("hook failure lost")
			}
			log, err := s.HookRunLog(repo, h.Name, run.ID)
			if err != nil || log.ExitCode != tc.exit || !strings.Contains(log.Tail, tc.want) {
				t.Fatalf("stored failure: %+v %v; want exit %d and %q", log, err, tc.exit, tc.want)
			}
		})
	}
}
