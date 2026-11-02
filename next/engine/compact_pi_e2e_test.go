//go:build linux

package engine

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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
