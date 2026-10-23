package subagent

import (
	"context"
	"errors"
	"testing"

	"dyna-agent/internal/room"
)

func TestRenameKeepsIdentityAndChecksReservedNames(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("RESPOND: a", func(o *StartOptions) { o.Name = "alpha" })
	a, _ = e.wait(a.ID, 1)

	renamed, err := e.m.Rename(context.Background(), a.ID, "gamma")
	if err != nil || renamed.ID != a.ID || renamed.EffectiveName() != "gamma" || renamed.Turns != a.Turns {
		t.Fatalf("Rename = %+v, %v", renamed, err)
	}
	for _, bad := range []string{"library", "bad name", "root"} {
		if _, err := e.m.Rename(context.Background(), a.ID, bad); err == nil {
			t.Fatalf("name %q was accepted", bad)
		}
	}
}

func TestRenameRefusesNameTakenInRoom(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("RESPOND: a", func(o *StartOptions) { o.Name = "alpha" })
	b, _ := e.start("RESPOND: b", func(o *StartOptions) { o.Name = "beta" })
	if _, err := e.m.Rename(context.Background(), b.ID, "alpha"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate open name: err = %v", err)
	}
	if same, err := e.m.Rename(context.Background(), a.ID, "alpha"); err != nil || same.EffectiveName() != "alpha" {
		t.Fatalf("same name = %+v, %v", same, err)
	}
}

func TestRenameAllowsSelfAndParentOnly(t *testing.T) {
	e := newTestEnv(t)
	lead, _ := e.start("RESPOND: lead", func(o *StartOptions) { o.Name = "lead" })
	lead, _ = e.wait(lead.ID, 1)
	other, _ := e.start("RESPOND: other", func(o *StartOptions) { o.Name = "other" })
	stampParent(t, lead)
	child, _ := e.start("RESPOND: child", func(o *StartOptions) { o.Name = "kid" })

	stampParent(t, child)
	if _, err := e.m.Rename(context.Background(), child.ID, "kid-self"); err != nil {
		t.Fatalf("session renames itself: %v", err)
	}
	if _, err := e.m.Rename(context.Background(), lead.ID, "lead-new"); err == nil {
		t.Fatal("child renamed its parent")
	}
	stampParent(t, other)
	if _, err := e.m.Rename(context.Background(), child.ID, "kid-stranger"); err == nil {
		t.Fatal("unrelated session renamed a child")
	}
	stampParent(t, lead)
	turnsBefore := lead.Turns
	if renamed, err := e.m.Rename(context.Background(), child.ID, "kid-parent"); err != nil || renamed.EffectiveName() != "kid-parent" {
		t.Fatalf("parent renames child = %+v, %v", renamed, err)
	}
	notes := hierarchyNotices(t, e)
	if len(notes) == 0 {
		t.Fatal("no rename notice")
	}
	last := notes[len(notes)-1]
	payload, ok := last.Session()
	if !ok || payload.Type != room.SessionRenamed || payload.OldName != "kid-self" || payload.Name != "kid-parent" || len(last.To) != 1 || last.To[0] != lead.Address() || last.Wake {
		t.Fatalf("rename notice = %+v", last)
	}
	if after, _ := e.m.Store.Info(lead.ID); after.Turns != turnsBefore || after.Status != lead.Status {
		t.Fatalf("parent was woken: before %+v after %+v", lead, after)
	}
}

func TestRenamedSessionKeepsPendingDelivery(t *testing.T) {
	e := newTestEnv(t)
	a, _ := e.start("RESPOND: a", func(o *StartOptions) { o.Name = "alpha" })
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("RESPOND: b", func(o *StartOptions) { o.Name = "beta" })
	oldAddr := a.Address()
	if _, err := e.m.Rename(context.Background(), a.ID, "gamma"); err != nil {
		t.Fatalf("rename alpha: %v", err)
	}
	if _, err := e.m.Rename(context.Background(), b.ID, "alpha"); err != nil {
		t.Fatalf("reuse old name: %v", err)
	}
	rooms := e.m.rooms()
	roomID := a.RoomID
	for _, in := range []room.PostInput{
		{From: "dyna", To: []string{oldAddr}, SessionIDs: map[string]string{oldAddr: a.ID}, Text: "for a under old name"},
		{From: "dyna", To: []string{b.Address()}, SessionIDs: map[string]string{b.Address(): b.ID}, Text: "for b under reused name"},
	} {
		if _, err := rooms.Post(roomID, in); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := rooms.Messages(roomID)
	if err != nil {
		t.Fatal(err)
	}
	metaA, err := e.m.store().ReadMeta(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	metaB, err := e.m.store().ReadMeta(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	texts := func(got []room.Message) []string {
		var out []string
		for _, m := range got {
			out = append(out, m.Text)
		}
		return out
	}
	if got := texts(mustForSession(t, rooms, metaA, msgs)); len(got) != 1 || got[0] != "for a under old name" {
		t.Fatalf("renamed session a got %q", got)
	}
	if got := texts(mustForSession(t, rooms, metaB, msgs)); len(got) != 1 || got[0] != "for b under reused name" {
		t.Fatalf("session b got %q", got)
	}
}
