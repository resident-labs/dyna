package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// A grok turn stopped before it wrote output; its record has no normEnd. The
// transferred opus turn then wrote the whole normalized file. Only the opus
// turn owns those events, in a page, a single-turn read and a live read.
func TestStoppedTurnWithoutOutputDoesNotReadTheNextTurn(t *testing.T) {
	store := &subagent.Store{Root: t.TempDir()}
	id := "ses_20261019-171826-a61fc417"
	dir, _ := store.Dir(id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	record := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	start := time.Date(2026, 10, 19, 17, 18, 28, 0, time.Local)
	recordtest.WriteRaw(t, store.Root, id, record(subagent.Meta{ID: id, Name: "compare-tools", Room: "dyna-agent", RoomID: "global", Profile: "opus", Dir: t.TempDir(), Depth: 1, Turns: 2, Created: start}))
	// The stored grok turn, as the runner wrote it: normStart 0, no normEnd.
	write(filepath.Join(dir, "turns", "1.json"), []byte(`{"n":1,"status":"stopped","profile":"grok","harness":"grok","prompt":"research","rawStart":0,"normStart":0,"started":"2026-10-19T17:18:28-05:00","ended":"2026-10-19T17:26:09-05:00"}`))
	output := `{"v":1,"kind":"status","name":"session","state":"started","ts":1792448775085}
{"v":1,"kind":"message","role":"user","text":"[DYNA MODEL TRANSFER] continue","ts":1792448777445}
{"v":1,"kind":"reasoning","state":"completed","text":"I should check the previous transcript.","ts":1792448777446}
{"v":1,"kind":"message","role":"assistant","text":"I'm starting the research now.","ts":1792448798353}
`
	normPath, _ := store.NormalizedPath(id)
	write(normPath, []byte(output))
	opus := subagent.Turn{N: 2, Status: subagent.TurnRunning, Profile: "opus", Prompt: "continue", NormStart: 0, Started: start.Add(8 * time.Minute)}
	write(filepath.Join(dir, "turns", "2.json"), record(opus))

	count := func(turn Turn) (n int) {
		for _, it := range turn.Items {
			if it.Kind == Thought || it.Kind == Reply {
				n++
			}
		}
		return n
	}
	check := func(when string) {
		t.Helper()
		loader := NewLoader(store)
		page, err := loader.LoadPage(id, 0, 6)
		if err != nil {
			t.Fatal(err)
		}
		if got := count(page.Turns[0]); got != 0 {
			t.Fatalf("%s: stopped turn 1 shows %d thoughts and replies of turn 2", when, got)
		}
		if got := count(page.Turns[1]); got != 2 {
			t.Fatalf("%s: turn 2 shows %d thoughts and replies, want 2", when, got)
		}
		one, err := loader.LoadTurn(id, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := count(one.Turns[0]); got != 0 {
			t.Fatalf("%s: the turn 1 link shows %d thoughts and replies of turn 2", when, got)
		}
	}
	check("while turn 2 runs")
	opus.Status, opus.Ended, opus.NormEnd = subagent.TurnOK, start.Add(36*time.Minute), int64(len(output))
	write(filepath.Join(dir, "turns", "2.json"), record(opus))
	check("after turn 2 ends")
}
