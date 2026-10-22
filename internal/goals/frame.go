package goals

import (
	"fmt"
	"strings"
)

func Clip(text string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return string(r)
}

// Frame stays in the changing turn context, outside the stable prompt prefix.
// counts maps a goal ID to its open and total item counts.
func Frame(all []Goal, id string, names map[string]string, counts map[string][2]int) string {
	open := false
	for _, g := range all {
		if g.Open() && g.Concerns(id) {
			open = true
			break
		}
	}
	if !open {
		return "[DYNA GOALS] None open for you. For work that takes more than one turn, set a goal first: `dyna goal --for @<you> \"<outcome; done when>\"`."
	}
	name := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	// Open goals only, the ones this session holds first. Done goals the
	// session set used to fill the short list and hide its open work.
	lines := []string{}
	ordered := make([]Goal, 0, len(all))
	for _, g := range all {
		if g.Open() && g.HeldBy(id) {
			ordered = append(ordered, g)
		}
	}
	for _, g := range all {
		if g.Open() && !g.HeldBy(id) {
			ordered = append(ordered, g)
		}
	}
	for _, g := range ordered {
		if !g.Concerns(id) {
			continue
		}
		text := g.ID + " " + Clip(g.Text, 160)
		if g.Shared {
			text += " · shared by the room"
		} else {
			text += " · from " + name(g.Setter)
		}
		if n := counts[g.ID]; n[1] > 0 {
			text += fmt.Sprintf(" · %d/%d items", n[0], n[1])
		}
		if g.Setter == id && !g.HeldBy(id) && !g.Shared {
			text = "Set by you: " + text
		}
		lines = append(lines, text)
	}
	// The header and overflow row count toward the five-line limit.
	if len(lines) > 4 {
		extra := len(lines) - 3
		lines = append(lines[:3], fmt.Sprintf("+%d more: dyna goal", extra))
	}
	return "[DYNA GOALS] `dyna goal show <id> --json` for detail. Name the goal ID in progress posts; use `dyna goal wait <id> --until <duration>` while waiting; `dyna goal done <id> \"<summary>\"` when finished.\n" + strings.Join(lines, "\n")
}
