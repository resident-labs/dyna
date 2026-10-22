package harness

import (
	"io"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

// Persistent stdin input (claude-stream-json): dyna keeps the worker's stdin
// open and writes the prompt, steering, and journal reminders as stream-json
// user messages. Claude takes a message written mid-turn into the
// conversation at its next tool boundary (a running tool is never
// interrupted) and starts a new turn in the same process for one written
// while idle. With --replay-user-messages it echoes each message back when it
// takes it, so dyna knows what was consumed. When a turn ends with nothing
// left unconsumed, Dyna seals steering admission and retires the process.
// EOF alone does not stop Claude while harness-native background tasks exist.

const (
	streamItemPrompt   = "prompt"
	streamItemSteering = "steering"
	streamItemJournal  = "journal"
)

type streamItem struct {
	kind     string
	text     string // exact written content, used to correlate replay acknowledgments
	messages []runstore.SteeringMessage
	written  time.Time
	inFlight int // tool calls in flight when it was written
}

type streamNotice struct {
	consumed *streamItem
	terminal *streamTerminal
}

// streamTerminal is the protocol boundary, not the end of the OS process.
// ready is evaluated at observation time: an acknowledgment AFTER this result
// cannot retroactively make that result cover the input.
type streamTerminal struct {
	line      string
	stdoutEnd int
	usage     workeroutput.Usage
	sequence  uint64
	ready     bool
}

type streamInput struct {
	w                io.WriteCloser
	mu               sync.Mutex
	closed           bool
	queue            []*streamItem // written, not yet echoed back
	notes            []streamNotice
	signal           chan struct{}
	last             time.Time       // last stdout line of any kind
	sequence         uint64          // increments on each successful write
	terminal         *streamTerminal // latched across empty, split, or coalesced drains
	generationActive bool            // affirmative activity since the last result; never time out quiet tools
	acked            map[string]bool // post IDs the worker acknowledged in this turn
}

// streamStallLimit: after a turn ends with written messages still not echoed
// back, dyna closes stdin if the worker prints nothing for this long (Claude
// starts the next turn, and prints, at once when it has queued input).
const streamStallLimit = 30 * time.Second

// emptyStepLimit: a worker that starts this many steps in a row without text,
// reasoning or tool output counts as failed. OpenCode models that loop emit
// one empty step about every 10 seconds, so this is about 60 seconds.
const emptyStepLimit = 6

// startupLimit bounds how long a turn may produce no harness event at all
// before dyna fails it instead of hanging. Any thinking, tool, usage or text
// event counts as startup progress, so silent-thinking models are safe. A
// worker that emits nothing is hung, as grok 1.0.62 was when its init stalled.
const startupLimit = 120 * time.Second

// quietFor reports how long stdout has been silent.
func (s *streamInput) quietFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.last)
}

func newStreamInput(w io.WriteCloser) *streamInput {
	return &streamInput{w: w, signal: make(chan struct{}, 1), acked: map[string]bool{}}
}

// write sends one user message. It returns false when stdin is closed.
func (s *streamInput) write(kind, text string, messages []runstore.SteeringMessage, inFlight int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if _, err := s.w.Write(workeroutput.ClaudeUserMessage(text)); err != nil {
		return false
	}
	s.sequence++
	s.queue = append(s.queue, &streamItem{text: text, kind: kind, messages: messages, written: time.Now(), inFlight: inFlight})
	return true
}

// observe is called for every stdout line (reader goroutine).
func (s *streamInput) observe(line string, stdoutEnd int, usage workeroutput.Usage) {
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
	replay, result := workeroutput.ClaudeInputSignal(line)
	if !replay && !result {
		if workeroutput.ClaudeGenerationStarted(line) {
			s.mu.Lock()
			s.generationActive = true
			s.mu.Unlock()
		}
		return
	}
	s.mu.Lock()
	if result {
		s.generationActive = false
		s.terminal = &streamTerminal{line: line, stdoutEnd: stdoutEnd, usage: usage, sequence: s.sequence, ready: len(s.queue) == 0}
		s.notes = append(s.notes, streamNotice{terminal: s.terminal})
	}
	// A replay acknowledges the oldest written items in order. Claude may
	// merge items and change whitespace, so posts are matched by the IDs in
	// their frame headers, not by exact text.
	if texts, ok := workeroutput.ClaudeReplayTexts(line); replay && ok {
		n := 0
		used := make([]bool, len(texts)) // each block holds one unframed item
		for n < len(s.queue) && replayCovers(s.queue[n], texts, used) {
			n++
		}
		for _, item := range s.queue[:n] {
			s.notes = append(s.notes, streamNotice{consumed: item})
			for _, m := range item.messages {
				if m.ID != "" {
					s.acked[m.ID] = true
				}
			}
		}
		if n > 0 {
			s.queue = s.queue[n:]
			s.generationActive = true
		}
	}
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s *streamInput) drain() []streamNotice {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.notes
	s.notes = nil
	return out
}

// canSeal requires a result covering every input written so far. It cannot
// change merely because notice batches or replay timing changed.
func (s *streamInput) canSeal(t *streamTerminal) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t != nil && t.ready && t.sequence == s.sequence && len(s.queue) == 0
}

// awaitingResult stays true after a terminal with outstanding input, even after
// that input is acknowledged: only a subsequent result can cover it.
func (s *streamInput) awaitingResult() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal != nil && (!s.terminal.ready || s.terminal.sequence != s.sequence || len(s.queue) > 0)
}

// awaitingAcknowledgment is the only silence-bound state. Once Claude has
// resumed generation, an uncovered result still cannot be committed, but the
// turn has no task deadline. A later result can rearm this input-only bound.
func (s *streamInput) awaitingAcknowledgment() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal != nil && len(s.queue) > 0 && !s.generationActive
}

func (s *streamInput) lastTerminal() *streamTerminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}

// replayCovers reports whether a replay holds a written item. An item with
// framed posts is held when the replay has each post's frame header
// ("[dyna message <id> "), so a post that only quotes another ID does not
// count. Other items (the prompt, journal reminders) match an unused block's
// text, ignoring trailing newlines; each block holds one such item.
func replayCovers(item *streamItem, texts []string, used []bool) bool {
	joined := strings.Join(texts, "\n")
	headers := 0
	for _, m := range item.messages {
		header := "[dyna message " + m.ID + " "
		if m.ID == "" || !strings.Contains(item.text, header) {
			continue
		}
		headers++
		if !strings.Contains(joined, header) {
			return false
		}
	}
	if headers > 0 {
		return true
	}
	for i, text := range texts {
		if !used[i] && strings.TrimRight(text, "\n") == strings.TrimRight(item.text, "\n") {
			used[i] = true
			return true
		}
	}
	return false
}

// pending returns written steering not acknowledged by the worker. These
// messages remain owned by this Dyna turn and must be resumed exactly.
func (s *streamInput) pending() (messages []runstore.SteeringMessage, journal bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.queue {
		for _, m := range item.messages {
			// A post acknowledged once in this turn is never resent.
			if m.ID == "" || !s.acked[m.ID] {
				messages = append(messages, m)
			}
		}
		journal = journal || item.kind == streamItemJournal
	}
	return
}

// idle reports whether every written message has been taken in.
func (s *streamInput) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue) == 0
}

func (s *streamInput) open() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}

// close ends input. Process retirement is separate: Claude may survive EOF.
func (s *streamInput) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		_ = s.w.Close()
	}
}
