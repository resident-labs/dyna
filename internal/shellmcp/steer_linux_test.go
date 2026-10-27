//go:build linux

package shellmcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D-349: a blocking wait must end when steering is queued for the calling
// session, so the harness reads the owner's message at the tool boundary
// instead of after the whole wait. The fake signal fires at once: the
// message is already queued when the wait starts. The call asks for the
// full 600 s against a job that never ends, so it can only return by
// cancelling the Store.Wait goroutine's context and joining it: a leaked
// waiter shows up as the bound, not as a silent pass.
func TestOutputEndsWhenSteeringArrives(t *testing.T) {
	tools := newTools(t, 50*time.Millisecond)
	gate, release := fifo(t)
	call(t, tools, "shell", map[string]any{"command": "cat " + gate, "description": "Wait at the gate"})
	job := onlyJob(t, tools)
	tools.Steer = func(session string) (<-chan struct{}, func()) {
		if session != tools.Owner {
			t.Errorf("steer signal watched %q, want %q", session, tools.Owner)
		}
		fired := make(chan struct{})
		close(fired)
		return fired, func() {}
	}
	answered := make(chan string, 1)
	go func() {
		answered <- call(t, tools, "shell_output", map[string]any{"job_id": job.ID, "wait": "exit", "timeout_seconds": 600})
	}()
	var text string
	select {
	case text = <-answered:
	case <-time.After(5 * time.Second): // safety bound; the signal fires at once
		t.Fatal("the steered wait did not end: the Store.Wait goroutine was not cancelled and joined")
	}
	if !strings.Contains(text, "Job "+job.ID+" is still running.") || !strings.Contains(text, steerNote) {
		t.Fatalf("steered wait answered %q", text)
	}
	// The job keeps running: after the message is read, a later wait still
	// finds it, here to its end.
	tools.Steer = nil
	release()
	if end := call(t, tools, "shell_output", map[string]any{"job_id": job.ID, "wait": "exit"}); !strings.HasPrefix(end, "Job "+job.ID+". Exit code 0.") {
		t.Fatalf("job after the steered wait = %q", end)
	}
}

// The real watch tails the session journal: a steer line appended after the
// watch starts fires it through the directory notification, without polling.
func TestWatchSteerJournalFiresOnQueuedSteering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"t\":\"turn_start\",\"turn\":1,\"ts\":1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fired, stop := WatchSteerJournal(path)
	defer stop()
	if fired == nil {
		t.Fatal("the steering watch did not start")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"t\":\"steer_submitted\",\"turn\":1,\"queued\":false,\"ts\":2}\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(5 * time.Second): // safety bound; the append wakes the watch
		t.Fatal("queued steering did not fire the watch")
	}
}

// steerSubmitted reads only complete new lines and only the steer kind.
func TestSteerSubmittedReadsOnlyNewQueuedSteering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	old := "{\"t\":\"steer_submitted\",\"turn\":1,\"ts\":1}\n"
	fresh := "{\"t\":\"turn_end\",\"turn\":2,\"ts\":2}\n{\"t\":\"steer_submitted\",\"turn\":2,\"ts\":3}\n"
	if err := os.WriteFile(path, []byte(old+fresh), 0600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(old))
	if !steerSubmitted(path, &offset) {
		t.Fatal("a new steer line did not report steering")
	}
	if want := int64(len(old + fresh)); offset != want {
		t.Fatalf("offset after the read = %d, want %d", offset, want)
	}
	// A trailing partial line waits for its newline and fires nothing.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"t\":\"steer_sub")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if steerSubmitted(path, &offset) {
		t.Fatal("a partial line reported steering")
	}
	if offset != int64(len(old+fresh)) {
		t.Fatalf("offset after a partial line = %d, want %d", offset, len(old+fresh))
	}
}
