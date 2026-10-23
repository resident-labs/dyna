package workeroutput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LastActivity names what a running worker does now, from the end of its
// normalized output, ignoring a torn last line.
func TestLastActivity(t *testing.T) {
	dir := t.TempDir()
	write := func(lines ...string) string {
		p := filepath.Join(dir, "output.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	filler := `{"v":1,"kind":"message","role":"assistant","text":"` + strings.Repeat("z", 20000) + `"}`
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"tool", []string{`{"v":1,"kind":"message","role":"assistant","text":"hi"}`, `{"v":1,"kind":"tool","name":"webfetch","input":"{\"url\":\"https://github.com/stablyai/orca\"}\nmore"}`, `{"v":1,"kind":"status","text":"step started"}`}, `webfetch: https://github.com/stablyai/orca`},
		{"mcp shell", []string{`{"v":1,"kind":"tool","name":"mcp__dyna__shell","input":"{\"command\":\"cat guide.md\",\"description\":\"Read guide command\"}"}`}, `Read guide command`},
		{"mcp read", []string{`{"v":1,"kind":"tool","name":"mcp__pi__read","input":"{\"file_path\":\"/repo/web/src/lib/notify.go\"}"}`}, `reading notify.go`},
		{"thinking", []string{filler, `{"v":1,"kind":"reasoning","text":"Let me check"}`}, "Thinking"},
		{"writing", []string{`{"v":1,"kind":"message","role":"assistant","text":"Done"}`}, "Writing"},
		{"torn last line", []string{`{"v":1,"kind":"tool","name":"bash","input":"go test ./..."}`, `{"v":1,"kind":"reason`}, "go test ./..."},
	} {
		if got := LastActivity(write(tc.lines...)).Text; got != tc.want {
			t.Errorf("%s: LastActivity = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := LastActivity(filepath.Join(dir, "missing.jsonl")); got != (Activity{}) {
		t.Errorf("missing file = %+v, want none", got)
	}
}

// A running tool reports when it started and its readable summary (the
// input's description, as Claude's Bash gives); a finished one reports no
// start. A compaction in progress is an activity too.
func TestLastActivityRunningToolAndCompaction(t *testing.T) {
	dir := t.TempDir()
	write := func(lines ...string) string {
		p := filepath.Join(dir, "output.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	start := `{"v":1,"kind":"tool","name":"Bash","state":"started","id":"t1","ts":1000,"input":"{\"command\":\"dd if=/dev/zero of=big bs=1M count=10000\",\"description\":\"Write a 10 GB test file\"}"}`
	running := LastActivity(write(start))
	if running.Text != "Write a 10 GB test file" || running.Since != 1000 {
		t.Fatalf("running tool = %+v", running)
	}
	done := LastActivity(write(start, `{"v":1,"kind":"tool","name":"tool_result","state":"completed","id":"t1","ts":2000,"output":"ok"}`))
	if done.Since != 0 {
		t.Fatalf("a finished tool still reports a start: %+v", done)
	}
	compacting := LastActivity(write(start, `{"v":1,"kind":"tool","name":"tool_result","state":"completed","id":"t1","ts":2000}`, `{"v":1,"kind":"status","name":"compaction","state":"started","ts":3000}`))
	if compacting.Text != "Compacting context…" || compacting.Since != 3000 {
		t.Fatalf("compaction = %+v", compacting)
	}
	compacted := LastActivity(write(`{"v":1,"kind":"status","name":"compaction","state":"started","ts":3000}`, `{"v":1,"kind":"status","name":"compaction","state":"completed","ts":4000}`))
	if compacted.Since != 0 {
		t.Fatalf("a finished compaction still reports a start: %+v", compacted)
	}
}

func TestLastActivityKeepsStartTimeThroughUpdates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "output.jsonl")
	write := func(lines ...string) string {
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	start := `{"v":1,"kind":"tool","name":"command_execution","state":"started","id":"c1","ts":1000,"input":"cargo build"}`
	update := `{"v":1,"kind":"tool","name":"command_execution","state":"updated","id":"c1","ts":5000,"input":"cargo build"}`
	thinking := `{"v":1,"kind":"reasoning","ts":900}`
	if a := LastActivity(write(thinking, start, update)); a.Text != "cargo build" || a.Since != 1000 {
		t.Fatalf("updated tool = %+v, want the start time 1000", a)
	}
	done := `{"v":1,"kind":"tool","name":"command_execution","state":"completed","id":"c1","ts":9000,"input":"cargo build"}`
	if a := LastActivity(write(thinking, start, update, done)); a.Since != 0 {
		t.Fatalf("a finished tool still reports a start: %+v", a)
	}
}
