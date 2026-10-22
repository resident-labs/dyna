package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestEndedPRNoticesAreInformational(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "merged"}[merged], func(t *testing.T) {
			e := newEnv(t)
			e.track()
			e.f.Pull.State, e.f.Pull.Merged = "closed", merged
			e.tick()
			n := lastNotice(t, e.record())
			if n.Kind != "informational" {
				t.Fatalf("ended PR notice kind = %q, want informational", n.Kind)
			}
		})
	}
}

func TestInformationalPRNoticeDoesNotWakeDormantAuthor(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	e.session(e.author.ID, e.author.ParentSession, true)
	if _, err := e.mgr.Store.UpdateMeta(e.author.ID, func(meta *subagent.Meta) {
		meta.Finished = e.clock
		meta.Status = subagent.StatusDormant
		meta.Turns = 1
	}); err != nil {
		t.Fatal(err)
	}
	turn, err := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnOK, Ended: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.mgr.Store.Root, e.author.ID, "turns", "1.json"), turn, 0600); err != nil {
		t.Fatal(err)
	}
	r := e.record()
	notice(&r, r.Author, "PR merged. Watching stopped.", "informational:merged")
	n := r.Notices[len(r.Notices)-1]
	id, err := e.s.deliver(context.Background(), r, n)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := e.rooms.Message(e.rec.ID, id)
	if err != nil || msg.Wake {
		t.Fatalf("informational notice must remain unread without waking: %+v %v", msg, err)
	}
	if _, err := e.mgr.TickSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := e.mgr.Store.Info(e.author.ID)
	if err != nil || after.Turns != 1 || after.Finished.IsZero() {
		t.Fatalf("informational notice woke dormant author: %+v %v", after, err)
	}
}

func TestUntargetedPRNoticeIsQuietUnlessRootMustAct(t *testing.T) {
	e := newEscalationEnv(t)
	e.track()
	r := e.record()
	for _, needsRoot := range []bool{false, true} {
		id, err := e.s.deliver(t.Context(), r, Notice{NeedsRoot: needsRoot, Text: "Batch needs a recovery decision."})
		if err != nil {
			t.Fatal(err)
		}
		msg, err := e.rooms.Message(e.rec.ID, id)
		if err != nil || msg.NeedsRoot != needsRoot || msg.Wake != needsRoot || room.WakesRoot(msg, e.rec.Name) != needsRoot {
			t.Fatalf("root notice needsRoot=%v: %+v %v", needsRoot, msg, err)
		}
	}
}
