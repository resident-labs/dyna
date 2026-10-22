package notify

import (
	"strings"
	"testing"
	"time"
)

// Asking, withdrawing and answering a question queue an instruction note
// through the production path (Ask, WithdrawQuestion, AnswerQuestion, then
// the engine's FlushInstructionNotes), so root's Open questions section
// reaches a running root as a drift note and its frozen prompt stays.
func TestQuestionChangesReachRootAsInstructionNotes(t *testing.T) {
	e := newTestEnv(t)
	rec := e.room()
	e.human(rec, "SLEEP:0 RESPOND: root ready", "@root")
	root := e.wait(e.rootSession(rec).ID, 1)
	frozen, err := e.mgr.Store.ReadMeta(root.ID)
	if err != nil || frozen.Instructions == nil {
		t.Fatalf("root has no frozen instructions: %v", err)
	}
	asRoot := Sender{Kind: SenderSession, Room: rec.Name, Session: root}
	lastNote := func(want string, samePrompt bool) {
		t.Helper()
		if _, err := e.mgr.FlushInstructionNotes(e.ctx, time.Now().Add(4*time.Second)); err != nil {
			t.Fatal(err)
		}
		meta, err := e.mgr.Store.ReadMeta(root.ID)
		if err != nil || len(meta.InstructionsNotes) == 0 {
			t.Fatalf("no instruction note: %v", err)
		}
		if samePrompt && meta.Instructions.Hash != frozen.Instructions.Hash {
			t.Fatal("a question change rebuilt root's prompt")
		}
		note, err := e.mgr.RoomStore().Message(rec.ID, meta.InstructionsNotes[len(meta.InstructionsNotes)-1])
		if err != nil || note.Kind != "instructions" || note.Wake || !strings.Contains(note.Text, want) {
			t.Fatalf("note %+v %v, want %q", note, err, want)
		}
	}

	q, err := e.svc.Ask(e.ctx, asRoot, "", "", "Which format?", "keep working", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	lastNote("Open questions: added \"- "+q.ID+" ·", true)
	if err := e.svc.WithdrawQuestion(asRoot, "", q.ID, "no longer needed"); err != nil {
		t.Fatal(err)
	}
	lastNote("Open questions: removed \"- "+q.ID+" ·", true)
	if meta, _ := e.mgr.Store.ReadMeta(root.ID); meta.Turns != 1 {
		t.Fatalf("a note started a turn: %d turns", meta.Turns)
	}

	answered, err := e.svc.Ask(e.ctx, asRoot, "", "", "Ship it?", "keep working", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	lastNote("Open questions: added \"- "+answered.ID+" ·", true)
	if err := e.svc.AnswerQuestion(e.ctx, Sender{Kind: SenderOwner, Room: rec.Name}, "", answered.ID, "", "yes"); err != nil {
		t.Fatal(err)
	}
	// The answer post also wakes root, and that turn may take a new snapshot,
	// so only the note is checked here.
	lastNote("Open questions: removed \"- "+answered.ID+" ·", false)
}
