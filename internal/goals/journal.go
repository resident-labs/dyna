package goals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// JournalLine is a saved status for one agent in a journal entry.
type JournalLine struct {
	Agent   string   `json:"agent"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Goals   []string `json:"goals"`
	// Done lists goals of this agent recorded as finished but
	// still open. Their holder is reminded once to close them.
	Done []string `json:"done,omitempty"`
	// Needs is a short, stable key for the action root or the owner must
	// take, such as "owner-pi-login". It stays the same while the action does.
	Needs string `json:"needs,omitempty"`
}

// An Event is one journal line about a goal or item: a note, or an item
// state change. Its ID is "e" and the byte offset of its line. Event lines
// carry kind "event"; a legacy keeper entry, converted by convertJournals in
// sqlite.go, carries no kind.
type Event struct {
	ID    string    `json:"id"`
	Goal  string    `json:"goal"`
	Item  string    `json:"item,omitempty"`
	By    string    `json:"by"`
	Kind  string    `json:"kind"` // always "event"
	Event string    `json:"event"`
	Text  string    `json:"text,omitempty"`
	At    time.Time `json:"at"`
}

// AddEvent appends one event line to the room's journal.
func (s *Store) AddEvent(room string, e Event) (Event, error) {
	path, err := s.journalPath(room)
	if err != nil {
		return Event{}, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Event{}, err
	}
	db, err := s.open()
	if err != nil {
		return Event{}, err
	}
	e.Kind = "event"
	// Journal rows have no change-stream consumer.
	err = state.For(db).AppendRecord(context.Background(), hub.Event{}, path, func(_ io.ReaderAt, offset int64) ([]byte, error) {
		e.ID = "e" + strconv.FormatInt(offset, 10)
		if e.At.IsZero() {
			e.At = s.now()
		}
		return json.Marshal(e)
	})
	if err != nil {
		return Event{}, err
	}
	return e, nil
}

// Events returns up to limit of the goal's events, newest first; limit <= 0
// returns all. A goal of "" returns every goal's events.
func (s *Store) Events(room, goal string, limit int) ([]Event, error) {
	out, _, err := s.EventsPage(room, goal, "", limit)
	return out, err
}

// JournalEntry is a saved report. Its ID is "j" and the byte
// offset of its line in the room's journal file, so a cursor finds its place
// without reading the whole file.
type JournalEntry struct {
	ID string `json:"id"`
	// Round is the check-in round that wrote the entry.
	Round       string        `json:"round,omitempty"`
	At          time.Time     `json:"at"`
	Entries     []JournalLine `json:"entries"`
	NeedsAction bool          `json:"needsAction"`
}

// NeedsAction reports whether any line asks root to act.
func NeedsAction(lines []JournalLine) bool {
	for _, l := range lines {
		switch l.Status {
		case "blocked", "stuck", "errored", "no_reply":
			return true
		}
	}
	return false
}

// journalPath is the room's append-only journal, one entry per line.
func (s *Store) journalPath(room string) (string, error) {
	if room == "" || room != filepath.Base(room) || strings.HasPrefix(room, ".") {
		return "", fmt.Errorf("invalid room ID %q", room)
	}
	return filepath.Join(s.Root, "journal", room+".jsonl"), nil
}

// AddJournal appends one pass entry to the room's journal. It sets the ID
// and NeedsAction, and At when it is zero. When an entry of the same round is
// already in the journal, AddJournal returns it instead, so a retry appends
// once.
func (s *Store) AddJournal(room string, e JournalEntry) (JournalEntry, error) {
	path, err := s.journalPath(room)
	if err != nil {
		return JournalEntry{}, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return JournalEntry{}, err
	}
	db, err := s.open()
	if err != nil {
		return JournalEntry{}, err
	}
	// Journal rows have no change-stream consumer.
	err = state.For(db).AppendRecord(context.Background(), hub.Event{}, path, func(r io.ReaderAt, offset int64) ([]byte, error) {
		if e.Round != "" {
			prior, ok, err := roundEntry(r, offset, e)
			if err != nil {
				return nil, err
			}
			if ok {
				e = prior
				return nil, nil
			}
		}
		e.ID, e.NeedsAction = "j"+strconv.FormatInt(offset, 10), NeedsAction(e.Entries)
		if e.At.IsZero() {
			e.At = s.now()
		}
		return json.Marshal(e)
	})
	if err != nil {
		return JournalEntry{}, err
	}
	return e, nil
}

// roundEntry finds an entry of e's round. Reports append rounds in time
// order, so a copy can only follow the entries older than e.At; the search
// stops at the first of those.
func roundEntry(r io.ReaderAt, size int64, e JournalEntry) (JournalEntry, bool, error) {
	cursor := ""
	for {
		page, older, err := readJournal(r, size, cursor, 8)
		if err != nil {
			return JournalEntry{}, false, err
		}
		for _, prior := range page {
			if prior.Round == e.Round {
				return prior, true, nil
			}
			if prior.At.Before(e.At) {
				return JournalEntry{}, false, nil
			}
		}
		if !older {
			return JournalEntry{}, false, nil
		}
		cursor = page[len(page)-1].ID
	}
}

// Journal returns up to limit entries, newest first; limit <= 0 returns all.
// A non-empty before returns only entries older than the entry with that ID.
// older reports whether entries remain before the last one returned.
func (s *Store) Journal(room, before string, limit int) (out []JournalEntry, older bool, err error) {
	out = []JournalEntry{}
	path, err := s.journalPath(room)
	if err != nil {
		return out, false, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, false, err
	}
	return readJournal(f, info.Size(), before, limit)
}

// readJournal reads a page from the same file snapshot for readers and retries.
func readJournal(r io.ReaderAt, size int64, before string, limit int) (out []JournalEntry, older bool, err error) {
	out = []JournalEntry{}
	older, err = readBackLines(r, size, "j", before, limit, func(line []byte) bool {
		var e JournalEntry
		var kind struct {
			Kind string `json:"kind"`
		}
		// Event lines share the file; a partial line from a crashed write is
		// skipped with them.
		if json.Unmarshal(line, &e) != nil || json.Unmarshal(line, &kind) != nil || kind.Kind != "" {
			return false
		}
		out = append(out, e)
		return true
	})
	return out, older, err
}

// readBackLines reads whole lines backward from the end, or from before the
// line that starts at the cursor offset named by prefix, in chunks. keep
// reports whether a line counts toward limit. It reports whether older lines
// remain.
func readBackLines(r io.ReaderAt, size int64, prefix, before string, limit int, keep func(line []byte) bool) (bool, error) {
	end := size
	if before != "" {
		parsed, err := strconv.ParseInt(strings.TrimPrefix(before, prefix), 10, 64)
		if err != nil || !strings.HasPrefix(before, prefix) || parsed < 0 || parsed > size || !lineStart(r, parsed) {
			return false, fmt.Errorf("journal entry %s does not exist in this room", before)
		}
		end = parsed
	}
	// Read backward in chunks; buf holds [pos, end) and ends at a line end.
	const chunk = 64 << 10
	var buf []byte
	pos := end
	kept := 0
	for len(buf) > 0 || pos > 0 {
		if limit > 0 && kept == limit {
			return len(buf) > 0 || pos > 0, nil
		}
		line := bytes.TrimSuffix(buf, []byte("\n"))
		i := bytes.LastIndexByte(line, '\n')
		if i < 0 && pos > 0 {
			n := min(int64(chunk), pos)
			pos -= n
			more := make([]byte, n)
			if _, err := r.ReadAt(more, pos); err != nil && err != io.EOF {
				return false, err
			}
			buf = append(more, buf...)
			continue
		}
		if keep(line[i+1:]) {
			kept++
		}
		buf = buf[:i+1]
	}
	return false, nil
}

// lineStart reports whether offset begins a line.
func lineStart(r io.ReaderAt, offset int64) bool {
	if offset == 0 {
		return true
	}
	b := make([]byte, 1)
	_, err := r.ReadAt(b, offset-1)
	return err == nil && b[0] == '\n'
}
