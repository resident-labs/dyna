package roomsvc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
)

func TestQuestionsAllRoomsReturnsOpenRowsByRoom(t *testing.T) {
	e := newEnv(t)
	a, b := e.room("alpha"), e.room("beta")
	archived := e.room("archived")
	store := e.svc.Notify.QuestionStore()
	ask := func(room, text string) questions.Question {
		q, err := store.Ask(questions.Question{Room: room, Asker: "owner", Text: text, Meanwhile: "Check other work"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	archivedQuestion := ask(archived.ID, "Open in archived room?")
	if err := e.svc.rooms().SetArchived(archived.ID, true); err != nil {
		t.Fatal(err)
	}
	openA := ask(a.ID, "Open alpha?")
	closed := ask(a.ID, "Closed alpha?")
	openB := ask(b.ID, "Open beta?")
	withdrawn := ask(b.ID, "Withdrawn beta?")
	if _, err := store.Answer(a.ID, closed.ID, questions.Answer{By: "owner", Text: "Done"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Withdraw(b.ID, withdrawn.ID, "owner", false, "No longer needed"); err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.Questions("")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var grouped map[string][]Question
	if err := json.Unmarshal(data, &grouped); err != nil {
		t.Fatal(err)
	}
	if len(grouped) != 2 || len(grouped[a.ID]) != 1 || grouped[a.ID][0].ID != openA.ID || len(grouped[b.ID]) != 1 || grouped[b.ID][0].ID != openB.ID {
		t.Fatalf("open rows: %+v", grouped)
	}
	if grouped[a.ID][0].AskerName != "root" || grouped[b.ID][0].AskerName != "root" {
		t.Fatalf("missing row names: %+v", grouped)
	}
	archivedRows, err := e.svc.Questions(archived.ID)
	if err != nil || len(archivedRows[archived.ID]) != 1 || archivedRows[archived.ID][0].ID != archivedQuestion.ID {
		t.Fatalf("archived room scope: %+v %v", archivedRows, err)
	}
	scoped, err := e.svc.Questions(a.ID)
	if err != nil || len(scoped) != 1 || len(scoped[a.ID]) != 2 {
		t.Fatalf("room history: %+v %v", scoped, err)
	}
}

func TestQuestionReadDoesNotCloneUnrelatedLog(t *testing.T) {
	const posts = 1500
	e := newEnv(t)
	r := e.room("large-log")
	var log bytes.Buffer
	encoder := json.NewEncoder(&log)
	for i := 0; i < posts; i++ {
		message := room.Message{ID: fmt.Sprintf("msg_%05d", i), From: r.Name, To: []string{r.Name}, Text: "An unrelated retained post"}
		if i == 0 {
			message.Attachments = []room.Attachment{{ID: "file_evidence", Name: "evidence.txt"}}
		}
		if err := encoder.Encode(message); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.svc.rooms().Root, r.ID, "messages.jsonl"), log.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Notify.QuestionStore().Ask(questions.Question{Room: r.ID, Asker: "owner", Text: "One question", Meanwhile: "Check other work"}, func(questions.Question) (string, error) { return "msg_00000", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Questions(r.ID); err != nil {
		t.Fatal(err)
	} // Warm the shared log index.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rows, err := e.svc.Questions(r.ID)
	runtime.ReadMemStats(&after)
	if err != nil || len(rows[r.ID]) != 1 || len(rows[r.ID][0].Attachments) != 1 {
		t.Fatalf("question attachment: %+v %v", rows, err)
	}
	// A clone of the indexed log costs at least one message per post; one
	// question read must stay well under half of that.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > posts*uint64(unsafe.Sizeof(room.Message{}))/2 {
		t.Fatalf("one question allocated %d bytes for an existing log index", allocated)
	}
}
