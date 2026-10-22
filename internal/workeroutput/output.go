// Package workeroutput defines Dyna's versioned worker-output contract and
// the allowlisted adapters that translate known harness records into it.
package workeroutput

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
)

const SchemaVersion = 1

const (
	DialectText                  = "text"
	DialectCodexJSONL            = "codex-jsonl"
	DialectAntigravityStreamJSON = "antigravity-stream-json"
	DialectOpenCodeJSON          = "opencode-json"
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
	DialectGrokJSON:              decodeGrok,
	DialectAcpJSON:               decodeAcp,
	DialectPiJSON:                decodePi,
	DialectText:                  decodeText,
	DialectCodexJSONL:            decodeCodex,
	DialectAntigravityStreamJSON: decodeAntigravity,
	DialectOpenCodeJSON:          decodeOpenCode,
	DialectClaudeStreamJSON:      decodeClaude,
	DialectMuseJSONL:             decodeMuse,
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
	decl        *Declaration
	w           io.Writer
	antigravity *antigravityStreamState
	// Now stamps events (tests); nil uses time.Now.
	Now func() time.Time
}

func NewNormalizer(decl *Declaration, w io.Writer) *Normalizer {
	n := &Normalizer{decl: CloneDeclaration(decl), w: w}
	if decl != nil && strings.TrimSpace(decl.Dialect) == DialectAntigravityStreamJSON {
		n.antigravity = &antigravityStreamState{}
	}
	return n
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
	events := record.Events
	if n.antigravity != nil {
		events = n.antigravity.deduplicate(events)
	}
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
		if _, err := n.w.Write(append(b, '\n')); err != nil {
			return err
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

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
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
