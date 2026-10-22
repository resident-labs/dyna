package subagent

import (
	"testing"

	"dyna-agent/internal/room"
)

// Only a post that names the session itself creates a report obligation. A
// group mention such as @team or @children wakes the session but owes nothing.
func TestOwesOnlyForDirectPostsFromParentOrOwner(t *testing.T) {
	m, _, work, metas := actorFixture(t)
	child := metas["child"]
	lead := metas["lead"].Room + ":" + metas["lead"].Name
	for _, tc := range []struct {
		name string
		msg  room.Message
		want bool
	}{
		{"owner names the session", room.Message{From: work.Name, Via: []string{"@child"}}, true},
		{"owner names the full address", room.Message{From: work.Name, Via: []string{"@work:child"}}, true},
		{"owner names the session ID", room.Message{From: work.Name, Via: []string{"@" + child.ID}}, true},
		{"owner names it beside a group", room.Message{From: work.Name, Via: []string{"@team", "@child"}}, true},
		{"owner post with no recorded mentions", room.Message{From: work.Name}, true},
		{"owner group broadcast", room.Message{From: work.Name, Via: []string{"@team"}}, false},
		{"owner names another agent beside a group", room.Message{From: work.Name, Via: []string{"@team", "@other"}}, false},
		{"parent names the child", room.Message{From: lead, Via: []string{"@child"}}, true},
		{"parent to all children", room.Message{From: lead, Via: []string{"@children"}}, false},
		{"sibling names the session", room.Message{From: "work:other", Via: []string{"@child"}}, false},
	} {
		if got := m.Store.Owes(child, tc.msg); got != tc.want {
			t.Errorf("%s: Owes = %v, want %v", tc.name, got, tc.want)
		}
	}
}
