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

// Note tells the worker how desired differs from the frozen snapshot, one
// line per changed section, or "" when they match. It goes at the end of the
// turn prompt and never into the frozen prefix.
func Note(frozen, desired Snapshot) string {
	if frozen.Hash == desired.Hash {
		return ""
	}
	find := func(s Snapshot, name string) (Section, bool) {
		i := slices.IndexFunc(s.Sections, func(sec Section) bool { return sec.Name == name })
		if i < 0 {
			return Section{}, false
		}
		return s.Sections[i], true
	}
	var lines []string
	seen := map[string]bool{}
	for _, sec := range append(slices.Clone(desired.Sections), frozen.Sections...) {
		if seen[sec.Name] {
			continue
		}
		seen[sec.Name] = true
		was, hadIt := find(frozen, sec.Name)
		now, hasIt := find(desired, sec.Name)
		switch {
		case !hasIt && len(was.Items) > 0:
			lines = append(lines, "- "+sec.Name+": removed "+quoted(was.Items)+".")
		case !hasIt:
			lines = append(lines, "- "+sec.Name+": removed.")
		case hadIt && was.body() == now.body():
		case len(now.Items) > 0 || len(was.Items) > 0:
			// A new section's heading is not news; its items are.
			if hadIt && was.Text != now.Text {
				lines = append(lines, "- "+sec.Name+": now:\n"+now.body())
				continue
			}
			var parts []string
			if added := missing(now.Items, was.Items); len(added) > 0 {
				parts = append(parts, "added "+quoted(added))
			}
			if removed := missing(was.Items, now.Items); len(removed) > 0 {
				parts = append(parts, "removed "+quoted(removed))
			}
			lines = append(lines, "- "+sec.Name+": "+strings.Join(parts, "; ")+".")
		case hadIt && paragraphChange(was.Text, now.Text) != "":
			lines = append(lines, "- "+sec.Name+": "+paragraphChange(was.Text, now.Text))
		default:
			lines = append(lines, "- "+sec.Name+": now:\n"+now.body())
		}
	}
	return "[DYNA INSTRUCTIONS CHANGED] Since this conversation started, your standing instructions changed. These changes apply now:\n" + strings.Join(lines, "\n")
}

// paragraphChange names the paragraphs added to and removed from a text
// section by their first lines, then gives the added paragraphs in full. It
// returns "" when the paragraphs only moved.
func paragraphChange(was, now string) string {
	paragraphs := func(text string) []string {
		var out []string
		for _, p := range strings.Split(text, "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	added := missing(paragraphs(now), paragraphs(was))
	removed := missing(paragraphs(was), paragraphs(now))
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	firstLines := func(ps []string) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
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
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "added "+quoted(firstLines(added)))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+quoted(firstLines(removed)))
	}
	out := strings.Join(parts, "; ") + "."
	if len(added) > 0 {
		out += "\nNew text:\n" + strings.Join(added, "\n\n")
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
