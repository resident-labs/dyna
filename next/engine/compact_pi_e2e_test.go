//go:build linux

package engine

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// compactProvider reports each turn's context at a set size and answers Pi's summary requests, which it can hold open.
type compactProvider struct {
	mu        sync.Mutex
	once      sync.Once
	tokens    int
	summaries int
	bodies    []string
	entered   chan struct{}
	release   chan struct{}
}

func (p *compactProvider) serve(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	summary := strings.Contains(string(data), "context summarization assistant")
	p.mu.Lock()
	p.bodies = append(p.bodies, string(data))
	tokens := p.tokens
	if summary {
		p.summaries++
		tokens = 1200
	}
	p.mu.Unlock()
	if summary && p.entered != nil {
		p.once.Do(func() { close(p.entered) })
		<-p.release
	}
	// Pi keeps the newest 20,000 tokens as they are, so a conversation has to outgrow that to have anything to summarize.
	text := strings.Repeat("Seen. ", 20000)
	if summary {
		text = "## Goal\nThe story so far."
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(name, payload string) { fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, payload) }
	send("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`, tokens))
	send("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	send("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, text))
	send("content_block_stop", `{"type":"content_block_stop","index":0}`)
	send("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	send("message_stop", `{"type":"message_stop"}`)
}

func (p *compactProvider) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodies[len(p.bodies)-1]
}

// realPiQuiet runs two real turns behind a real runner, then moves the clock past the quiet wait.
func realPiQuiet(t *testing.T, provider *compactProvider) (*piBox, *dormantHost, func() []byte) {
	t.Helper()
	b := newPiBox(t, provider.serve, nil)
	host := &dormantHost{}
	b.s.boxes = host
	var clock atomic.Int64
	clock.Store(time.Unix(1000, 0).UnixMilli())
	b.s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	resetLifecycle(b.s)
	b.post(t, "the first thing")
	h, _ := b.start(t, nil)
	b.waitTurn(t)
	b.post(t, "the second thing")
	b.waitTurn(t)
	clock.Add((11 * time.Minute).Milliseconds())
	return b, host, func() []byte { return sessionBytes(t, h) }
}

func compactionEvents(t *testing.T, b *piBox) []contract.SessionCompacted {
	t.Helper()
	events, err := b.s.store.Events(b.ctx, b.room, 0, 256, []contract.EventType{"session.compacted"})
	if err != nil {
		t.Fatal(err)
	}
	var out []contract.SessionCompacted
	for _, event := range events {
		out = append(out, event.Body.(contract.SessionCompacted))
	}
	return out
}

func waitCompaction(t *testing.T, b *piBox) contract.SessionCompacted {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if recorded := compactionEvents(t, b); len(recorded) > 0 {
			return recorded[0]
		}
	}
	t.Fatal("no compaction was recorded")
	return contract.SessionCompacted{}
}

// TestRealPiCompactsBeforeAQuietStop runs real Pi against the fake upstream, outside a container: the reconciler's quiet stop
// asks over the real channel, Pi measures and summarizes on its own, and the box stops only after the answer.
func TestRealPiCompactsBeforeAQuietStop(t *testing.T) {
	provider := &compactProvider{tokens: 150000}
	b, host, transcript := realPiQuiet(t, provider)
	passLifecycle(t, b.s, b.ctx)
	recorded := waitCompaction(t, b)
	if recorded.Error != "" || recorded.TokensBefore <= idleCompactTokens || recorded.TokensAfter >= recorded.TokensBefore || recorded.Usage.InputTokens < 1200 || recorded.Usage.Model != "m" {
		t.Fatalf("recorded %+v", recorded)
	}
	passLifecycle(t, b.s, b.ctx)
	if host.stops != 1 {
		t.Fatalf("%d stops", host.stops)
	}
	if provider.summaries == 0 || !bytes.Contains(transcript(), []byte(`"type":"compaction"`)) {
		t.Fatalf("%d summaries, transcript %s", provider.summaries, transcript())
	}
}

func TestRealPiLeavesASmallContextAlone(t *testing.T) {
	provider := &compactProvider{tokens: 5000}
	b, host, transcript := realPiQuiet(t, provider)
	for deadline := time.Now().Add(time.Minute); host.stops == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		passLifecycle(t, b.s, b.ctx)
	}
	if host.stops != 1 || provider.summaries != 0 || len(compactionEvents(t, b)) != 0 || bytes.Contains(transcript(), []byte(`"type":"compaction"`)) {
		t.Fatalf("%d stops, %d summaries", host.stops, provider.summaries)
	}
}

func TestRealPiRunsAWakeThatLandsDuringACompaction(t *testing.T) {
	provider := &compactProvider{tokens: 150000, entered: make(chan struct{}), release: make(chan struct{})}
	b, host, transcript := realPiQuiet(t, provider)
	passLifecycle(t, b.s, b.ctx)
	select {
	case <-provider.entered:
	case <-time.After(time.Minute):
		t.Fatalf("no summary request, recorded %+v", compactionEvents(t, b))
	}
	b.post(t, "are you there?")
	passLifecycle(t, b.s, b.ctx)
	close(provider.release)
	b.waitTurn(t)
	if recorded := waitCompaction(t, b); recorded.Error != "" {
		t.Fatalf("recorded %+v", recorded)
	}
	passLifecycle(t, b.s, b.ctx)
	if host.stops != 0 || !strings.Contains(provider.last(), "are you there?") || !strings.Contains(provider.last(), "The story so far.") {
		t.Fatalf("%d stops, last request %s", host.stops, provider.last())
	}
	if !bytes.Contains(transcript(), []byte(`"type":"compaction"`)) {
		t.Fatal("the compaction was lost")
	}
}
