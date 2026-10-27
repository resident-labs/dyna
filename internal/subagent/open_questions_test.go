package subagent

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/instructions"
	"dyna-agent/internal/questions"
)

func TestRootSeesOpenQuestionsAndAnAnswerIsADriftNote(t *testing.T) {
	e := newTestEnv(t)
	store := &questions.Store{Root: filepath.Join(filepath.Dir(e.m.store().root()), "questions")}
	ask := func(text string) questions.Question {
		t.Helper()
		q, err := store.Ask(questions.Question{Room: e.room.ID, Asker: e.room.Name, AskerKind: questions.AskerOwner, To: "owner", Text: text, Meanwhile: "keep working"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	first, second := ask("Which format?"), ask("Ship  the\nbeta now?")
	root := Meta{ID: "ses_20261020-000002-aaaaaaaa", Name: RoleRoot, Room: e.room.Name, RoomID: e.room.ID, Role: RoleRoot, Depth: 1}
	frozen := e.m.sessionInstructions(root, "owner", 3, "")
	text := frozen.Text()
	for _, want := range []string{
		"[DYNA OPEN QUESTIONS]",
		"- " + first.ID + " · asked by " + e.room.Name + " · " + first.AskedAt.UTC().Format("2006-01-02 15:04 UTC") + " · Which format?",
		"· Ship the beta now?",
		"dyna ask withdraw <id> --why",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("root instructions lack %q:\n%s", want, text)
		}
	}
	worker := root
	worker.Role, worker.Name = "", "worker"
	if strings.Contains(e.m.sessionInstructions(worker, "owner", 3, "").Text(), "[DYNA OPEN QUESTIONS]") {
		t.Fatal("a worker session got root's open questions")
	}
	if again := e.m.sessionInstructions(root, "owner", 3, ""); again.Hash != frozen.Hash {
		t.Fatal("unchanged questions changed the snapshot")
	}

	// An answer reaches root as a drift note; the conversation keeps its
	// frozen prompt, because nothing here forces a refresh.
	if _, err := store.Answer(e.room.ID, second.ID, questions.Answer{By: "owner", Human: true, Text: "yes"}); err != nil {
		t.Fatal(err)
	}
	if instructions.Refresh(frozen, false, false, false) {
		t.Fatal("an answered question rebuilt the prompt")
	}
	note := instructions.Note(frozen, e.m.sessionInstructions(root, "owner", 3, ""))
	if !strings.Contains(note, "- Open questions: removed \"- "+second.ID+" ·") || strings.Contains(note, first.ID) {
		t.Fatalf("note:\n%s", note)
	}
}

// Through the session's own note path: answering a question posts an
// instructions note to root and leaves its frozen snapshot as it was.
func TestAnsweredQuestionReachesRunningRootAsANote(t *testing.T) {
	e := newTestEnv(t)
	store := &questions.Store{Root: filepath.Join(filepath.Dir(e.m.store().root()), "questions")}
	q, err := store.Ask(questions.Question{Room: e.room.ID, Asker: e.room.Name, AskerKind: questions.AskerOwner, To: "owner", Text: "Which format?", Meanwhile: "keep working"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := e.start("SLEEP:0\nRESPOND: ready", func(o *StartOptions) { o.Role, o.Name = RoleRoot, RoleRoot })
	e.wait(root.ID, 1)
	before, err := e.m.Store.ReadMeta(root.ID)
	if err != nil || before.Instructions == nil || !strings.Contains(before.Instructions.Text(), q.ID) {
		t.Fatalf("frozen root instructions lack the question: %v", err)
	}
	if _, err := store.Answer(e.room.ID, q.ID, questions.Answer{By: "owner", Human: true, Text: "JSON"}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.noteInstructions(e.ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	after, err := e.m.Store.ReadMeta(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Instructions.Hash != before.Instructions.Hash || after.Turns != 1 || len(after.InstructionsNotes) != 1 {
		t.Fatalf("the answer rebuilt the prompt or posted no note: notes %v, turns %d", after.InstructionsNotes, after.Turns)
	}
	note, err := e.m.rooms().Message(e.room.ID, after.InstructionsNotes[0])
	if err != nil || note.Kind != "session" || note.Notice.Kind != "instructions" || note.Wake || !strings.Contains(note.Text, "Open questions: removed \"- "+q.ID) {
		t.Fatalf("note: %+v %v", note, err)
	}
}
