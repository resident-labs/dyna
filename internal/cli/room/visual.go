package room

import (
	"fmt"
	"io"
	"strings"

	"dyna-agent/internal/room"
	"dyna-agent/internal/visual"
)

// preview renders a card in this, the posting, process. Tests that do not
// check the preview replace it, so they skip a browser launch.
var preview = visual.Preview

// printVisual tells the posting agent which visual and version it made,
// what the checks found and where the preview images are, so it can look
// at them and fix the layout before anyone else sees it.
func printVisual(w io.Writer, rooms *room.Store, m room.Message, a room.Attachment) {
	id := a.VisualID(m.ID)
	counts := map[string]int{}
	for _, c := range a.Checks {
		counts[c.Status]++
	}
	var sum []string
	for _, s := range []struct{ status, word string }{{"pass", "passed"}, {"warn", "warning"}, {"fail", "failed"}, {"skip", "skipped"}} {
		if n := counts[s.status]; n > 0 {
			word := s.word
			if s.status == "warn" && n > 1 {
				word += "s"
			}
			sum = append(sum, fmt.Sprintf("%d %s", n, word))
		}
	}
	fmt.Fprintf(w, "  visual %s v%d; checks: %s\n", id, a.Version, strings.Join(sum, ", "))
	skipped := false
	for _, c := range a.Checks {
		switch {
		case c.Status == "skip" && skipped:
		case c.Status != "pass":
			fmt.Fprintf(w, "    %s %s: %s\n", c.Status, c.Name, c.Detail)
			skipped = skipped || c.Status == "skip"
		}
	}
	path := func(id string) string {
		p, err := rooms.AttachmentPath(m.RoomID, id)
		if err != nil {
			return id
		}
		return p
	}
	if a.Banner != "" || len(a.Shots) > 0 {
		fmt.Fprintln(w, "  previews; open them to check the layout:")
		if a.Banner != "" {
			fmt.Fprintf(w, "    banner   %s\n", path(a.Banner))
		}
		for _, s := range a.Shots {
			fmt.Fprintf(w, "    %-8s %s\n", fmt.Sprintf("%d px", s.Width), path(s.ID))
		}
	}
	fmt.Fprintf(w, "  update it in place: dyna post <@mention> --html <file> --update %s \"<what changed>\"\n", id)
}
