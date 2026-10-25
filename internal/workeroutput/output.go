// Package workeroutput defines Dyna's versioned worker-output contract and
// the allowlisted adapters that translate known harness records into it.
package workeroutput

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const SchemaVersion = 1

const (
	DialectText         = "text"
	DialectCodexJSONL   = "codex-jsonl"
	DialectOpenCodeJSON = "opencode-json"
)

// Declaration is the optional, profile-specific description of a model's
// stdout. Dialect names resolve only through the registry below; profiles
// cannot inject executable adapter code.
type Declaration struct {
	Dialect string `json:"dialect"`
}

// Event is one line in an output.jsonl artifact. V versions the normalized
// contract independently of the source dialect.
// docs/reasoning-and-compaction.md defines the reasoning and compaction events.
type Event struct {
	// SourceOffset is the original output line boundary, assigned by readers.
	SourceOffset int64 `json:"-"`

	V      int    `json:"v"`
	Kind   string `json:"kind"` // message|reasoning|tool|status|raw
	Role   string `json:"role,omitempty"`
	Name   string `json:"name,omitempty"`
	State  string `json:"state,omitempty"`
	Text   string `json:"text,omitempty"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
	Raw    string `json:"raw,omitempty"`
	// ID identifies a streamed response or pairs a tool call with its
	// completion, when the dialect provides one.
	ID string `json:"id,omitempty"`
	// Exit is a completed command's exit code, when the dialect reports it.
	Exit *int `json:"exit,omitempty"`
	// A status event named compaction carries how it was asked for and how
	// large the context was on each side of it. Trigger is "auto", "manual"
	// or "dyna" when Dyna ran the harness's compact command itself. The
	// token counts are set only when the source reports them.
	Trigger      string `json:"trigger,omitempty"`
	TokensBefore int64  `json:"tokensBefore,omitempty"`
	TokensAfter  int64  `json:"tokensAfter,omitempty"`
	// TS is when dyna read the record (unix ms). Older output has none.
	TS int64 `json:"ts,omitempty"`
}

// ProfileSnapshot is the output-relevant portion of one profile frozen for a
// run or a selected agent invocation.
type ProfileSnapshot struct {
	Name   string       `json:"name"`
	Model  string       `json:"model,omitempty"`
	Output *Declaration `json:"output,omitempty"`
}

// RunSnapshot freezes every profile output declaration visible at run start.
type RunSnapshot struct {
	Version  int               `json:"version"`
	Profiles []ProfileSnapshot `json:"profiles"`
}

// SelectedSnapshot freezes the declaration selected for one invocation.
type SelectedSnapshot struct {
	Version int             `json:"version"`
	Profile ProfileSnapshot `json:"profile"`
}

type adapter func([]byte) Record

var adapters = map[string]adapter{
	DialectPiJSON:           decodePi,
	DialectText:             decodeText,
	DialectCodexJSONL:       decodeCodex,
	DialectOpenCodeJSON:     decodeOpenCode,
	DialectClaudeStreamJSON: decodeClaude,
	DialectMuseJSONL:        decodeMuse,
}

// Dialects returns the stable allowlist accepted by profile declarations.
func Dialects() []string {
	out := make([]string, 0, len(adapters))
	for dialect := range adapters {
		out = append(out, dialect)
	}
	sort.Strings(out)
	return out
}

func ValidateDeclaration(decl *Declaration) error {
	if decl == nil {
		return nil
	}
	dialect := strings.TrimSpace(decl.Dialect)
	if dialect == "" {
		return fmt.Errorf("output dialect is required when output is declared")
	}
	if _, ok := adapters[dialect]; !ok {
		return fmt.Errorf("unknown output dialect %q (valid: %s)", dialect, strings.Join(Dialects(), ", "))
	}
	return nil
}

// NormalizeLine adapts one source record. Completed tool calls are normally
// omitted because their matching start events already describe the work without
// duplicating potentially large result payloads. Dialects that split calls and
// results across records may retain them. Missing declarations, unknown dialects
// in future snapshots, malformed JSON, and unsupported record types all become
// lossless raw events.
func NormalizeLine(decl *Declaration, line string) []Event {
	return DecodeLine(decl, line).Events
}

// Normalizer writes normalized JSONL without participating in harness
// success/failure. Callers deliberately ignore errors so observability can
// never change the worker's final result.
type Normalizer struct {
	mu       sync.Mutex
	boundary int64
	known    bool
	decl     *Declaration
	w        io.Writer
	// Now stamps events (tests); nil uses time.Now.
	Now func() time.Time
}

// NewNormalizer appends to w. The caller must give it exclusive write access.
// A seekable writer is positioned at its absolute end. Retained output must
// end in a verified newline before its boundary can be reported.
func NewNormalizer(decl *Declaration, w io.Writer) *Normalizer {
	n := &Normalizer{decl: CloneDeclaration(decl), w: w}
	if seeker, ok := w.(io.Seeker); ok {
		if end, err := seeker.Seek(0, io.SeekEnd); err == nil && end >= 0 {
			n.boundary = end
			n.known = end == 0 || endsLine(w, end)
		}
	}
	return n
}

func endsLine(w io.Writer, end int64) bool {
	r, ok := w.(io.ReaderAt)
	if f, isFile := w.(*os.File); isFile {
		// Output files are normally opened write-only. Verify the read handle
		// still names the same file before inspecting the last byte.
		read, err := os.Open(f.Name())
		if err != nil {
			return false
		}
		defer read.Close()
		original, err := f.Stat()
		if err != nil {
			return false
		}
		opened, err := read.Stat()
		if err != nil || !os.SameFile(original, opened) {
			return false
		}
		r, ok = read, true
	}
	if !ok {
		return false
	}
	var last [1]byte
	n, err := r.ReadAt(last[:], end-1)
	return n == 1 && err == nil && last[0] == '\n'
}

// Boundary returns the absolute end of complete normalized lines. Unknown
// initial positions and failed writes remain unknown for this normalizer.
func (n *Normalizer) Boundary() (int64, bool) {
	if n == nil {
		return 0, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.boundary, n.known
}

func (n *Normalizer) WriteLine(line string) error {
	if n == nil || n.w == nil {
		return nil
	}
	return n.WriteRecord(DecodeLine(n.decl, line))
}

// WriteRecord writes the presentation events of an already decoded record.
func (n *Normalizer) WriteRecord(record Record) error {
	if n == nil || n.w == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	events := record.Events
	now := time.Now().UnixMilli()
	if n.Now != nil {
		now = n.Now().UnixMilli()
	}
	for _, event := range events {
		event.TS = now
		b, err := json.Marshal(event)
		if err != nil {
			return err
		}
		line := append(b, '\n')
		written, err := n.w.Write(line)
		if err == nil && written != len(line) {
			err = io.ErrShortWrite
		}
		if err != nil {
			n.known = false
			return err
		}
		if n.known {
			n.boundary += int64(written)
		}
	}
	return nil
}

func CloneDeclaration(decl *Declaration) *Declaration {
	if decl == nil {
		return nil
	}
	copy := *decl
	return &copy
}

func adaptText(line []byte) ([]Event, bool) {
	return []Event{{Kind: "message", Role: "assistant", Text: string(line)}}, true
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(b)
}

// PairTools folds each tool completion into its start (matched by ID): the
// start gains the completion's Output, Exit, and State, and the completion is
// dropped. A completion whose ID matches no start folds into the oldest
// still-open start with the same tool name instead: artifacts written before
// the muse adapter keyed starts by call id pair a task-id start with a
// call-id result, and same-name calls complete in start order there.
// Completions with neither stay as they are. The second result maps each
// folded start's index to the completion time (TS), so a reader can show
// durations.
func PairTools(events []Event) ([]Event, map[int]int64) {
	out := make([]Event, 0, len(events))
	open := map[string]int{}
	openByName := map[string][]int{}
	ended := map[int]int64{}
	closeStart := func(i int) {
		delete(open, out[i].ID)
		queue := openByName[out[i].Name]
		for j, k := range queue {
			if k == i {
				openByName[out[i].Name] = append(queue[:j], queue[j+1:]...)
				break
			}
		}
	}
	fold := func(i int, e Event) {
		s := &out[i]
		if e.Output != "" {
			s.Output = e.Output
		}
		if s.Input == "" {
			s.Input = e.Input
		}
		if e.Exit != nil {
			s.Exit = e.Exit
		}
		s.State = e.State
		ended[i] = e.TS
		closeStart(i)
	}
	for _, e := range events {
		if e.Kind == "tool" && e.ID != "" {
			switch e.State {
			case "started", "updated":
				if i, ok := open[e.ID]; ok && e.State == "updated" {
					if e.Output != "" {
						out[i].Output = e.Output
					}
					continue
				}
				if _, ok := open[e.ID]; !ok {
					openByName[e.Name] = append(openByName[e.Name], len(out))
				}
				open[e.ID] = len(out)
			case "completed", "failed":
				if i, ok := open[e.ID]; ok {
					fold(i, e)
					continue
				}
				// TODO: legacy/shim remove later — pairs old Muse task IDs with result call IDs by tool name; remove after affected artifacts expire or migrate
				if queue := openByName[e.Name]; len(queue) > 0 {
					fold(queue[0], e)
					continue
				}
			}
		}
		out = append(out, e)
	}
	return out, ended
}

// IsCompactionEvent reports whether e is a compaction status record. Dyna
// keeps every one of them when it trims a long turn, because the divider is
// the only sign the context was compacted.
func IsCompactionEvent(e Event) bool {
	return e.Kind == "status" && (e.Name == "compaction" || e.Name == "compact_boundary")
}

// KeepCompactions trims events to their newest max records, keeping every
// compaction status event where it was. A long turn keeps only its tail, so a
// compaction early in it would otherwise disappear from every view.
func KeepCompactions(events []Event, max int) []Event {
	if max <= 0 || len(events) <= max {
		return events
	}
	first := len(events) - max
	out := make([]Event, 0, max)
	for i, e := range events {
		if i >= first || IsCompactionEvent(e) {
			out = append(out, e)
		}
	}
	return out
}

// CoalesceNormalizedEvents turns a provider's token-sized reasoning and message
// deltas into readable blocks without changing the append-only output artifact.
// Consecutive deltas of one kind that share an ID merge into a single block, so
// streamed replies read as whole messages instead of word fragments.
func CoalesceNormalizedEvents(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, event := range events {
		if event.V == SchemaVersion && event.State == "delta" && (event.Kind == "reasoning" || event.Kind == "message") {
			if len(out) > 0 {
				last := &out[len(out)-1]
				if last.V == event.V && last.Kind == event.Kind && last.State == "delta" && last.ID == event.ID {
					last.Text += event.Text
					continue
				}
			}
		}
		if event.V == SchemaVersion && event.Kind == "status" && event.Name == "reasoning" && event.State == "completed" && len(out) > 0 {
			last := &out[len(out)-1]
			if last.V == event.V && last.Kind == "reasoning" && last.State == "delta" {
				last.State = "completed"
				continue
			}
		}
		out = append(out, event)
	}
	return out
}
