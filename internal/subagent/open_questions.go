package subagent

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/questions"
)

// openQuestions lists root's open questions and call requests in its room.
// Each item holds a fixed time, so changes reach root as drift notes.
func (m *Manager) openQuestions(meta Meta) instructions.Section {
	section := instructions.Section{Name: "Open questions"}
	if meta.Role != RoleRoot {
		return section
	}
	asked, err := (&questions.Store{Root: filepath.Join(filepath.Dir(m.store().root()), "questions")}).List(meta.RoomID)
	if err != nil {
		return section
	}
	// The store returns rows unordered; a stable order keeps the snapshot hash.
	sort.Slice(asked, func(i, j int) bool {
		if !asked[i].AskedAt.Equal(asked[j].AskedAt) {
			return asked[i].AskedAt.Before(asked[j].AskedAt)
		}
		return asked[i].ID < asked[j].ID
	})
	for _, q := range asked {
		if q.State != "open" {
			continue
		}
		text := strings.Join(strings.Fields(q.Text), " ")
		if r := []rune(text); len(r) > 120 {
			text = string(r[:119]) + "…"
		}
		section.Items = append(section.Items, fmt.Sprintf("- %s · asked by %s · %s · %s", q.ID, q.Asker, q.AskedAt.UTC().Format("2006-01-02 15:04 UTC"), text))
	}
	calls, err := (&callrequests.Store{Root: filepath.Dir(m.store().root())}).List(meta.RoomID)
	if err == nil {
		for _, request := range calls {
			if request.State != "ringing" {
				continue
			}
			reason := strings.Join(strings.Fields(request.Reason), " ")
			section.Items = append(section.Items, fmt.Sprintf("- %s · call request from %s · %s · %s", request.ID, request.Asker, request.AskedAt.UTC().Format("2006-01-02 15:04 UTC"), reason))
		}
	}
	if len(section.Items) > 0 {
		section.Text = "[DYNA OPEN QUESTIONS]\nYour open questions and call requests to the owner. Answers and call results arrive as posts. Withdraw a call with `dyna call withdraw <id>`. Withdraw a question that has gone stale with `dyna request withdraw <id> --why \"<reason>\"`."
	}
	return section
}
