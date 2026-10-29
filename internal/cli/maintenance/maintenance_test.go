package maintenance

import (
	"bytes"
	"strings"
	"testing"
	"time"

	m "dyna-agent/internal/maintenance"
)

func TestStatusListsRecentPassesNewestFirst(t *testing.T) {
	dir := t.TempDir()
	cmd := statusCommand(func() string { return dir })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "No maintenance passes recorded yet.") {
		t.Fatalf("empty status = %q, %v", out.String(), err)
	}
	for n, freed := range []int64{1 << 20, 3 << 30} {
		var r m.Result
		r.Kept = map[string]int{}
		r.Pruned = map[string]m.Pruned{"closed session stdout": {Items: n + 1, Bytes: freed}}
		r.BytesFreed = freed
		if err := m.SaveReport(dir, time.Unix(int64(n)*3600, 0), time.Second, r, nil); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	cmd = statusCommand(func() string { return dir })
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--last", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "freed 3.0 GB: closed session stdout 2 (3.0 GB)") {
		t.Fatalf("status --last 1 = %q", out.String())
	}
}

// A pass's timed steps print under it with their error, so a deadline
// failure names the step that ran out of time.
func TestStatusStepsNameTheStepThatFailed(t *testing.T) {
	dir := t.TempDir()
	var r m.Result
	r.Steps = []m.Step{
		{Name: "worktrees", Took: 40 * time.Millisecond},
		{Name: "scratch", Took: 2 * time.Minute, Error: "context deadline exceeded"},
	}
	if err := m.SaveReport(dir, time.Unix(0, 0), time.Second, r, nil); err != nil {
		t.Fatal(err)
	}
	cmd := statusCommand(func() string { return dir })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--steps"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "    worktrees  40ms\n    scratch  2m0s  error: context deadline exceeded\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Fatalf("status --steps = %q, want suffix %q", out.String(), want)
	}
}
