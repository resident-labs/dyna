package hookinstall

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/processguard"
)

func TestQueuedInstallsRestartOnlyNewestCommit(t *testing.T) {
	s, first := fixture(t)
	first.SHA = strings.Repeat("b", 40)
	first.Status = "running"
	first.StartedAt = s.now()
	if err := s.Jobs.Update(first.ID, func(j *Job) error { *j = first; return nil }); err != nil {
		t.Fatal(err)
	}
	second, newest := first, first
	second.ID, second.SHA, second.Status = strings.Repeat("2", 32), strings.Repeat("c", 40), "queued"
	newest.ID, newest.SHA, newest.Status = strings.Repeat("3", 32), testSHA, "queued"
	if err := os.MkdirAll(filepath.Join(first.Config.Build, "web"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Config.Build, "web", "package.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls []string
	fake := fakeInstaller(t, first, "main", "", &calls)
	queued := false
	head := first.SHA
	now := first.StartedAt
	s.Now = func() time.Time { now = now.Add(time.Second); return now }
	s.Exec = func(ctx context.Context, p processguard.Process) error {
		if strings.Contains(strings.Join(p.Args, " "), "rev-parse origin/main") {
			fmt.Fprintln(p.Output, head)
			return nil
		}
		if len(p.Args) > 3 && p.Args[3] == "go" && !queued {
			queued = true
			// Two merges arrive while the first binary is still being built.
			if err := s.Start(ctx, second, &bytes.Buffer{}); err != nil {
				return err
			}
			if err := s.Start(ctx, newest, &bytes.Buffer{}); err != nil {
				return err
			}
			head = newest.SHA
		}
		return fake(ctx, p)
	}
	for _, j := range []Job{first, second, newest} {
		if err := s.Locked(t.Context(), j.ID, "", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if err := s.Jobs.Update(j.ID, func(j *Job) error { j.Status = "finished"; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(strings.Join(calls, "\n"), "restart dyna-engine.service"); got != 1 {
		t.Fatalf("engine restart count=%d, want 1 for newest commit; calls:\n%v", got, calls)
	}
	installed, err := os.ReadFile(first.Config.State)
	if err != nil || strings.TrimSpace(string(installed)) != newest.SHA {
		t.Fatalf("installed=%q error=%v, want newest %s", installed, err, newest.SHA)
	}
	for _, old := range []Job{first, second} {
		receipt, err := s.Jobs.Read(old.ID)
		if err != nil || receipt.Installed != "" {
			t.Fatalf("superseded job installed: %+v %v", receipt, err)
		}
	}
}
