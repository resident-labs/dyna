//go:build linux

package engine

import (
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// A session in a real box on real Podman, with the real box agent and the Pi the image ships, against the fake upstream: a
// message to a busy session joins its running turn after the current tool call, a foreground command steps aside for it and
// still wakes the session when it ends, and messages that arrive during one call reach the model together.
func TestMessagesJoinARunningTurnInARealBox(t *testing.T) {
	provider := &toolUpstream{tools: map[string][]crewTool{
		"task-steer": {{"bash", jsonOf(t, map[string]any{"command": "touch /tmp/started; sleep 12; echo slept", "description": "Slow"})}},
		"task-batch": {{"bash", jsonOf(t, map[string]any{"command": "echo quick"})}},
	}}
	b := newToolBox(t, boxProfiles{}, provider)
	b.start(t)
	post := func(text string) {
		t.Helper()
		if _, problem := b.s.Post(b.ctx, b.owner, contract.PostRequest{Room: b.room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: b.session}, Text: text}); problem != nil {
			t.Fatal(problem)
		}
	}
	// requests returns each model request, oldest first.
	requests := func() []string {
		provider.mu.Lock()
		defer provider.mu.Unlock()
		return append([]string(nil), provider.bodies...)
	}

	// The brief's turn ends first. Then an idle session starts a turn as ever, and its command waits in the foreground until
	// a message arrives.
	b.waitTurns(t, "the brief", 1)
	before := len(requests())
	post("task-steer")
	for exec.Command("podman", "exec", string(b.id), "test", "-e", "/tmp/started").Run() != nil {
		if b.ctx.Err() != nil {
			t.Fatal("the command never started")
		}
		time.Sleep(200 * time.Millisecond)
	}
	sent := time.Now()
	post("note-one")
	b.waitTurns(t, "the steered turn", 2)
	bodies := requests()[before:]
	if len(bodies) != 2 {
		t.Fatalf("%d model requests, want the call and one after it", len(bodies))
	}
	after := bodies[1]
	result := provider.lastOf(provider.results(t))
	moved := regexp.MustCompile(`\[job (job_[0-9a-f]+)\] The owner sent you a message, so this command moved to the background, where it keeps running\. job output reads its new output, and Dyna will wake you when it completes\.$`).FindStringSubmatch(result.Text)
	if result.IsError || moved == nil {
		t.Fatalf("the moved command: %+v", result)
	}
	job := contract.ShellJobID(moved[1])
	// The message follows the call's result in the very next request, and the command did not wait out its sleep.
	if i, j := strings.Index(after, "The owner sent you a message"), strings.Index(after, "note-one"); i < 0 || j < i {
		t.Fatalf("the message is not after the result: %s", after)
	}
	if took := time.Since(sent); took > 10*time.Second {
		t.Fatalf("the turn took %v after the message", took)
	}
	if !strings.Contains(after, `Your final response goes to the owner in your DM.\nnote-one`) {
		t.Fatalf("the steer's frame: %s", after)
	}

	// The job keeps running and wakes the session when it ends.
	b.waitTurns(t, "the job's wake", 3)
	listed, problem := b.s.EventsList(b.ctx, b.owner, contract.EventsListRequest{Room: b.room, Types: []contract.EventType{"shell.job.ended"}, Limit: 10})
	if problem != nil {
		t.Fatal(problem)
	}
	if len(listed.Events) != 1 {
		t.Fatalf("job ends %+v", listed.Events)
	}
	if end := listed.Events[0].Body.(contract.ShellJobEnded); end.Job != job || end.End.Kind != contract.ShellJobExited || end.End.Code != 0 || !strings.Contains(end.Tail, "slept") {
		t.Fatalf("the job's end %+v", end)
	}
	if body := provider.lastBody(); !strings.Contains(body, string(job)) || !strings.Contains(body, "slept") {
		t.Fatalf("the wake does not name the job: %s", body)
	}

	// Two messages that arrive while the model works reach it together, after its call.
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	provider.mu.Lock()
	provider.hold = func() { once.Do(func() { close(reached); <-release }) }
	provider.mu.Unlock()
	before = len(requests())
	post("task-batch")
	<-reached
	post("note-two")
	post("note-three")
	for b.s.storeCount(t, "SELECT COUNT(*) FROM deliveries WHERE queue='steer' AND state='taken'") != 2 {
		if b.ctx.Err() != nil {
			t.Fatal("the messages never went to the box")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // the box passes them on to Pi
	close(release)
	b.waitTurns(t, "the batched turn", 4)
	bodies = requests()[before:]
	if len(bodies) != 2 {
		t.Fatalf("%d model requests, want the call and one after it", len(bodies))
	}
	if after := bodies[1]; !strings.Contains(after, "note-two") || !strings.Contains(after, "note-three") {
		t.Fatalf("the request after the call: %s", after)
	}
	// The call started after both messages, so it ran to its end.
	if result := provider.lastOf(provider.results(t)); result.IsError || !strings.HasPrefix(result.Text, "quick\n[job ") || !strings.HasSuffix(result.Text, ": exit 0]") {
		t.Fatalf("the call: %+v", result)
	}
}
