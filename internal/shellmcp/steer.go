package shellmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/shells"
)

// steerNote is the line a wait interrupted by steering adds to its answer.
// It tells the model why the wait ended and how to keep waiting.
const steerNote = "A new message arrived for you; read it, then call shell_output again to keep waiting."

// steerKind is the journal kind the engine appends to a session's
// events.jsonl when it queues a post for that session's running turn: the
// subagent package's JournalSteerSubmitted. Named here so this server does
// not import the whole session CLI for one constant.
const steerKind = "steer_submitted"

// WatchSteerJournal reports when steering is queued for the session whose
// event journal lives at path. It returns a channel that closes on the
// first steer_submitted line appended after the watch starts, and a stop
// function that ends the watch; call it before the caller goes away. The
// journal only ever grows by appended lines, so the watch tails it from its
// current size through directory notification, never polling. A watch that
// cannot start reports a nil channel: waits then run uninterrupted.
func WatchSteerJournal(path string) (<-chan struct{}, func()) {
	// Read the start size before the watch is registered, so a line that
	// lands in between is at worst seen by the next periodic re-check
	// instead of skipped as already read.
	var offset int64
	if st, err := os.Stat(path); err == nil {
		offset = st.Size()
	}
	w, err := shells.WatchDir(filepath.Dir(path))
	if err != nil {
		log.Printf("shellmcp: steering watch on %s: %v", path, err)
		return nil, func() {}
	}
	fired, stopped, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		defer w.Close()
		for {
			// One second bounds a missed notification; a real change or a
			// stop wakes Next at once.
			if err := w.Next(time.Second); err != nil {
				return
			}
			select {
			case <-stopped:
				return
			default:
			}
			if steerSubmitted(path, &offset) {
				close(fired)
				return
			}
		}
	}()
	var once bool
	return fired, func() {
		if !once {
			once = true
			close(stopped)
			w.Wake()
			<-finished
		}
	}
}

// steerSubmitted reads complete lines appended to the journal at path past
// *offset and reports whether one records queued steering. It advances
// *offset past what it read, so a partial line is read again once complete.
func steerSubmitted(path string, offset *int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() <= *offset {
		return false
	}
	buf := make([]byte, st.Size()-*offset)
	n, err := f.ReadAt(buf, *offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	data := buf[:n]
	complete := bytes.LastIndexByte(data, '\n')
	if complete < 0 {
		return false
	}
	*offset += int64(complete) + 1
	var line struct {
		T string `json:"t"`
	}
	for _, b := range bytes.Split(data[:complete], []byte{'\n'}) {
		if len(b) == 0 || json.Unmarshal(b, &line) != nil {
			continue
		}
		if line.T == steerKind {
			return true
		}
	}
	return false
}
