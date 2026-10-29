// Package instructions builds the standing instructions of a Dyna session or
// worker: one snapshot, frozen for the life of a harness conversation and
// passed byte for byte on every run, so the cached prompt prefix holds. When
// the live state moves on, Note describes the change in the turn prompt.
package instructions

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
)

// Section is one named part of the instructions. Items are its separately
// tracked lines, such as rules; a change note names them one by one.
type Section struct {
	Name  string   `json:"name"`
	Text  string   `json:"text,omitempty"`
	Items []string `json:"items,omitempty"`
}

func (s Section) body() string {
	return strings.TrimSpace(strings.Join(append([]string{s.Text}, s.Items...), "\n"))
}

// Snapshot is the instructions for one conversation.
type Snapshot struct {
	Sections []Section `json:"sections,omitempty"`
	Hash     string    `json:"hash,omitempty"`
}

// Build assembles a snapshot from its parts in the given order. Empty parts
// are left out. The same parts always give the same bytes.
func Build(parts ...Section) Snapshot {
	var s Snapshot
	for _, p := range parts {
		if p.body() != "" {
			s.Sections = append(s.Sections, p)
		}
	}
	if len(s.Sections) > 0 {
		s.Hash = fmt.Sprintf("%x", sha256.Sum256([]byte(s.Text())))
	}
	return s
}

// Text is the instructions as the harness receives them.
func (s Snapshot) Text() string {
	bodies := make([]string, len(s.Sections))
	for i, sec := range s.Sections {
		bodies[i] = sec.body()
	}
	return strings.Join(bodies, "\n\n")
}

// Refresh reports whether a conversation takes a new snapshot this turn. Only
// where the cached prefix is already lost: a new conversation, the first turn
// after compaction, or a transfer or consolidation.
func Refresh(frozen Snapshot, newConversation, compacted, transferred bool) bool {
	return frozen.Hash == "" || newConversation || compacted || transferred
}

// ChangeAction is what happened to one section of the standing instructions.
type ChangeAction string

// Change actions.
const (
	// ChangeItems is a section whose tracked lines changed.
	ChangeItems ChangeAction = "items"
	// ChangeBody is a section whose text changed.
	ChangeBody ChangeAction = "body"
	// ChangeRemoved is a section that is gone.
	ChangeRemoved ChangeAction = "removed"
)

// Change is one section that a note names. It carries the facts, so the note
// text, an event payload and a web row all read the same change.
type Change struct {
	Name   string
	Action ChangeAction
	// Added and Removed are the tracked lines the section gained and lost.
	Added   []string
	Removed []string
	// Body is the section's new text, for a whole-text change.
	Body string
}

// Note tells the worker how desired differs from the frozen snapshot, one
// line per changed section, or "" when they match. It goes at the end of the
// turn prompt and never into the frozen prefix.
func Note(frozen, desired Snapshot) string {
	changes := Changes(frozen, desired)
	if len(changes) == 0 {
		return ""
	}
	return NoteHeader + "\n" + Lines(changes)
}

// NoteHeader is the first line of the note. A typed event states it as its
// one-line summary, so the agent text and the note stay one text.
const NoteHeader = "[DYNA INSTRUCTIONS CHANGED] Since this conversation started, your standing instructions changed. These changes apply now:"

// Lines renders the changed sections, one line each, without the header.
func Lines(changes []Change) string {
	lines := make([]string, 0, len(changes))
	for _, change := range changes {
		var parts []string
		if len(change.Added) > 0 {
			parts = append(parts, "added "+quoted(change.Added))
		}
		if len(change.Removed) > 0 {
			parts = append(parts, "removed "+quoted(change.Removed))
		}
		switch {
		case change.Action == ChangeRemoved && len(parts) == 0:
			lines = append(lines, "- "+change.Name+": removed.")
		case change.Action == ChangeRemoved:
			lines = append(lines, "- "+change.Name+": "+strings.Join(parts, "; ")+".")
		case len(parts) == 0:
			lines = append(lines, "- "+change.Name+": now:\n"+change.Body)
		case change.Action == ChangeBody:
			lines = append(lines, "- "+change.Name+": "+strings.Join(parts, "; ")+".\nNew text:\n"+change.Body)
		default:
			lines = append(lines, "- "+change.Name+": "+strings.Join(parts, "; ")+".")
		}
	}
	return strings.Join(lines, "\n")
}

// Changes lists the sections that differ between the frozen instructions and
// the desired ones, in the order the note names them.
func Changes(frozen, desired Snapshot) []Change {
	if frozen.Hash == desired.Hash {
		return nil
	}
	find := func(s Snapshot, name string) (Section, bool) {
		i := slices.IndexFunc(s.Sections, func(sec Section) bool { return sec.Name == name })
		if i < 0 {
			return Section{}, false
		}
		return s.Sections[i], true
	}
	var out []Change
	seen := map[string]bool{}
	for _, sec := range append(slices.Clone(desired.Sections), frozen.Sections...) {
		if seen[sec.Name] {
			continue
		}
		seen[sec.Name] = true
		was, hadIt := find(frozen, sec.Name)
		now, hasIt := find(desired, sec.Name)
		switch {
		case !hasIt:
			out = append(out, Change{Name: sec.Name, Action: ChangeRemoved, Removed: was.Items})
		case hadIt && was.body() == now.body():
		case len(now.Items) > 0 || len(was.Items) > 0:
			// A new section's heading is not news; its items are.
			if hadIt && was.Text != now.Text {
				out = append(out, Change{Name: sec.Name, Action: ChangeBody, Body: now.body()})
				continue
			}
			added, removed := missing(now.Items, was.Items), missing(was.Items, now.Items)
			if len(added) == 0 && len(removed) == 0 {
				// Only the order moved, so no line names the change. Show the new text.
				out = append(out, Change{Name: sec.Name, Action: ChangeBody, Body: now.body()})
				continue
			}
			out = append(out, Change{Name: sec.Name, Action: ChangeItems, Added: added, Removed: removed})
		case hadIt && paragraphChange(was.Text, now.Text) != "":
			added, removed, body := paragraphChangeParts(was.Text, now.Text)
			out = append(out, Change{Name: sec.Name, Action: ChangeBody, Added: added, Removed: removed, Body: body})
		default:
			out = append(out, Change{Name: sec.Name, Action: ChangeBody, Body: now.body()})
		}
	}
	return out
}

// paragraphChange names the paragraphs added to and removed from a text
// section by their first lines, then gives the added paragraphs in full. It
// returns "" when the paragraphs only moved.
func paragraphChange(was, now string) string {
	added, removed, body := paragraphChangeParts(was, now)
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+quoted(added))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+quoted(removed))
	}
	out := strings.Join(parts, "; ") + "."
	if len(added) > 0 {
		out += "\nNew text:\n" + body
	}
	return out
}

// paragraphChangeParts names the paragraphs added and removed by their first
// lines, and returns the added paragraphs as the new text.
func paragraphChangeParts(was, now string) (added, removed []string, body string) {
	paragraphs := func(text string) []string {
		var out []string
		for _, p := range strings.Split(text, "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	newParagraphs, oldParagraphs := paragraphs(now), paragraphs(was)
	addedText := missing(newParagraphs, oldParagraphs)
	removedText := missing(oldParagraphs, newParagraphs)
	if len(addedText) == 0 && len(removedText) == 0 {
		return nil, nil, ""
	}
	return firstLines(addedText), firstLines(removedText), strings.Join(addedText, "\n\n")
}

// firstLines names each paragraph by its first line, clipped so a long one
// stays short in the note.
func firstLines(paragraphs []string) []string {
	out := make([]string, len(paragraphs))
	for i, p := range paragraphs {
		line, _, more := strings.Cut(p, "\n")
		if r := []rune(line); len(r) > 100 {
			line, more = string(r[:100]), true
		}
		if more {
			line += "…"
		}
		out[i] = line
	}
	return out
}

func missing(items, from []string) []string {
	var out []string
	for _, item := range items {
		if !slices.Contains(from, item) {
			out = append(out, item)
		}
	}
	return out
}

func quoted(items []string) string {
	q := make([]string, len(items))
	for i, item := range items {
		q[i] = fmt.Sprintf("%q", item)
	}
	return strings.Join(q, ", ")
}

// Clone returns a deep copy, or nil for nil.
func (s *Snapshot) Clone() *Snapshot {
	if s == nil {
		return nil
	}
	out := Snapshot{Hash: s.Hash, Sections: slices.Clone(s.Sections)}
	for i := range out.Sections {
		out.Sections[i].Items = slices.Clone(out.Sections[i].Items)
	}
	return &out
}
