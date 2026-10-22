package subagent

import (
	"context"
	"testing"

	"dyna-agent/internal/room"
)

func TestDirectMentionFollowsBindingNotName(t *testing.T) {
	gamma := Meta{ID: "a", Name: "gamma", Room: "dyna-agent", Service: true}
	alpha := Meta{ID: "b", Name: "alpha", Room: "dyna-agent", Service: true}
	// A post written before a rename: To and Via keep the old name "alpha".
	direct := room.Message{From: "dyna-agent:worker", To: []string{"dyna-agent:alpha"}, Via: []string{"@alpha"}, SessionIDs: map[string]string{"dyna-agent:alpha": "a"}}
	group := room.Message{From: "dyna-agent:worker", To: []string{"dyna-agent:alpha"}, Via: []string{"@team"}, SessionIDs: map[string]string{"dyna-agent:alpha": "a"}}
	if !gamma.ServiceContract().DirectMention(direct) {
		t.Fatal("renamed service lost direct mention")
	}
	if alpha.ServiceContract().DirectMention(direct) {
		t.Fatal("name holder without the binding got a direct mention")
	}
	if gamma.ServiceContract().DirectMention(group) {
		t.Fatal("group selector granted a direct mention")
	}
}

func TestRenamedParentKeepsDirectReportDuty(t *testing.T) {
	e := newTestEnv(t)
	parent, _ := e.start("RESPOND: p", func(o *StartOptions) { o.Name = "lead" })
	child, _ := e.start("RESPOND: c", func(o *StartOptions) { o.Name = "kid" })
	if _, err := e.m.Store.UpdateMeta(child.ID, func(m *Meta) { m.ParentSession = parent.ID }); err != nil {
		t.Fatal(err)
	}
	childOld, parentOld := child.Address(), parent.Address()
	ownerPost := room.Message{From: child.Room, To: []string{childOld}, Via: []string{"@kid"}, SessionIDs: map[string]string{childOld: child.ID}}
	parentPost := room.Message{From: parentOld, To: []string{childOld}, Via: []string{"@kid"}, SessionIDs: map[string]string{childOld: child.ID, parentOld: parent.ID}}
	group := room.Message{From: parentOld, To: []string{childOld}, Via: []string{"@children"}, SessionIDs: map[string]string{childOld: child.ID, parentOld: parent.ID}}
	if _, err := e.m.Rename(context.Background(), child.ID, "kid-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Rename(context.Background(), parent.ID, "lead-new"); err != nil {
		t.Fatal(err)
	}
	meta, err := e.m.store().ReadMeta(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := e.m.store()
	if !store.Owes(meta, ownerPost) {
		t.Fatal("owner work to the renamed child owes no report")
	}
	if !store.Owes(meta, parentPost) {
		t.Fatal("renamed parent's direct request owes no report")
	}
	if store.Owes(meta, group) {
		t.Fatal("group mention created a report duty")
	}
}
