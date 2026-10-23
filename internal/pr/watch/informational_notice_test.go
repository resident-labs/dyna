package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestEndedPRNoticesWakeAuthorOrOpenAncestor(t *testing.T) {
	for _, merged := range []bool{false, true} {
		outcome := map[bool]string{false: "closed", true: "merged"}[merged]
		for _, route := range []string{"child", "top-level", "closed-author", "closed-parent", "closed-chain"} {
			t.Run(outcome+"/"+route, func(t *testing.T) {
				e := newEscalationEnv(t)
				e.testEnv.track()
				recipient, _ := e.mgr.Store.Info(e.author.ID)
				switch route {
				case "top-level":
					e.session(e.author.ID, "", true)
					recipient, _ = e.mgr.Store.Info(e.author.ID)
				case "closed-author":
					e.session(e.author.ID, e.author.ParentSession, false)
					recipient, _ = e.mgr.Store.Info(e.author.ParentSession)
				case "closed-parent":
					e.session(e.author.ID, e.author.ParentSession, false)
					grandparent := "ses_20261017-000008-dddddddd"
					e.session(grandparent, "", true)
					e.session(e.author.ParentSession, grandparent, false)
					recipient, _ = e.mgr.Store.Info(grandparent)
				case "closed-chain":
					e.session(e.author.ID, e.author.ParentSession, false)
					e.session(e.author.ParentSession, "", false)
					recipient = subagent.Info{}
				}
				e.f.Pull.State, e.f.Pull.Merged = "closed", merged
				e.f.Pull.Head.Ref = "feature"
				e.f.Pull.Base.Repo.DefaultBranch = "main"
				e.s.CleanupMerged = func(context.Context, Record, string) error { return nil }
				e.tick()
				r := e.record()
				var completion Notice
				for _, n := range r.Notices {
					if n.Kind == outcome {
						completion = n
					}
				}
				if completion.ID == "" {
					t.Fatalf("no %s notice: %+v", outcome, r.Notices)
				}
				r.Head += "-new"
				r.Watching = true
				endRecord(&r, merged, e.clock)
				count := 0
				for _, n := range r.Notices {
					if n.Kind == outcome {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("got %d completion notices", count)
				}
				id, err := e.s.deliver(t.Context(), r, completion)
				if err != nil {
					t.Fatal(err)
				}
				msg, err := e.rooms.Message(e.rec.ID, id)
				want, wake := recipient.Address(), true
				if route == "closed-chain" {
					want, wake = e.rec.Name, false
				}
				if err != nil || msg.Wake != wake || len(msg.To) != 1 || msg.To[0] != want {
					t.Fatalf("completion recipient: %+v %v, want %s wake=%v", msg, err, want, wake)
				}
				if len(e.starts) != 0 {
					t.Fatalf("ended change started an agent: %+v", e.starts)
				}
			})
		}
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
		if err != nil || msg.NeedsRoot != needsRoot || msg.Wake != needsRoot || wakesRoot(msg, e.rec.Name) != needsRoot {
			t.Fatalf("root notice needsRoot=%v: %+v %v", needsRoot, msg, err)
		}
	}
}
