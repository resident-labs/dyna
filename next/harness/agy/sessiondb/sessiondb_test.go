package sessiondb

import (
	"os"
	"path/filepath"
	"testing"
)

const sid = "11111111-2222-4333-8444-555555555555"

func TestLogReadsOnlyStepsAddedByThePrompt(t *testing.T) {
	dir := t.TempDir()
	// An old error step, before the prompt, is not the prompt's.
	if err := AppendStep(dir, sid, StepError, "Error 500, Message: old, Status: INTERNAL"); err != nil {
		t.Fatal(err)
	}
	l, warning := Open(dir, sid)
	if l == nil || warning != "" {
		t.Fatalf("open: %v %q", l, warning)
	}
	if step, _ := l.Check(); step != nil {
		t.Fatalf("no step was added: %+v", step)
	}
	AppendStep(dir, sid, StepPlannerResponse, "answer")
	AppendStep(dir, sid, StepError, "Error 404, Message: gone, Status: NOT_FOUND")
	step, warning := l.Check()
	if warning != "" || step == nil || !step.IsError || !step.ReachedModel || step.Error != "Error 404, Message: gone, Status: NOT_FOUND" {
		t.Fatalf("step = %+v %q", step, warning)
	}
}

func TestLogCreatedByThePrompt(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, sid)
	if l == nil {
		// no conversations directory yet: not provably this session's own file
		os.MkdirAll(filepath.Join(dir, "antigravity-acp", "conversations"), 0o700)
		l, _ = Open(dir, sid)
	}
	AppendStep(dir, sid, StepError, "boom")
	if step, _ := l.Check(); step == nil || step.Error != "boom" || step.ReachedModel {
		t.Fatalf("step = %+v", step)
	}
}

func TestLinkedLogIsNotRead(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	AppendStep(other, sid, StepError, "foreign")
	conv := filepath.Join(dir, "antigravity-acp", "conversations")
	os.MkdirAll(conv, 0o700)
	os.Symlink(filepath.Join(other, "antigravity-acp", "conversations", sid+".db"), filepath.Join(conv, sid+".db"))
	if l, warning := Open(dir, sid); l != nil || warning == "" {
		t.Fatalf("a linked database was opened: %v %q", l, warning)
	}
	if l, _ := Open(dir, "../etc"); l != nil {
		t.Fatal("a session id with a path was accepted")
	}
}

func TestLogIsOpenedReadOnly(t *testing.T) {
	dir := t.TempDir()
	AppendStep(dir, sid, StepError, "x")
	path := filepath.Join(dir, "antigravity-acp", "conversations", sid+".db")
	before, _ := os.ReadFile(path)
	l, _ := Open(dir, sid)
	l.Check()
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the database changed")
	}
}

func write(t *testing.T, dir string, rel string, data string) {
	t.Helper()
	path := filepath.Join(dir, filepath.Join(brain(sid)...), rel)
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPendingReply(t *testing.T) {
	dir := t.TempDir()
	if PendingReply(dir, sid) {
		t.Fatal("no records, no reply")
	}
	write(t, dir, "messages/m1.json", `{"id":"m1","priority":"MESSAGE_PRIORITY_HIGH"}`)
	if !PendingReply(dir, sid) {
		t.Fatal("an unread high-priority message wakes agy")
	}
	write(t, dir, "messages/read.json", `{"m1":true}`)
	if PendingReply(dir, sid) {
		t.Fatal("a read message does not")
	}
	write(t, dir, "logs/transcript.jsonl", `{"step_index":1,"type":"PLANNER_RESPONSE"}`+"\n"+`{"step_index":2,"type":"SYSTEM_MESSAGE","content":"x priority=MESSAGE_PRIORITY_HIGH"}`+"\n")
	if !PendingReply(dir, sid) {
		t.Fatal("a dequeued system message with no response yet is a pending reply")
	}
	write(t, dir, "logs/transcript.jsonl", `{"step_index":2,"type":"SYSTEM_MESSAGE","content":"priority=MESSAGE_PRIORITY_LOW"}`+"\n")
	if PendingReply(dir, sid) {
		t.Fatal("a low-priority one does not")
	}
}

func TestChildrenAtWork(t *testing.T) {
	dir := t.TempDir()
	child := "child-1"
	write(t, dir, "subagents/"+child+".json", `{"conversationId":"child-1","state":"SUBAGENT_STATE_RUNNING"}`)
	childMsg := filepath.Join(dir, filepath.Join(brain(child)...), "messages", "m.json")
	os.MkdirAll(filepath.Dir(childMsg), 0o700)
	os.WriteFile(childMsg, []byte(`{"sender":"`+sid+`","sourceMetadata":{"tool":{"conversationId":"`+sid+`","toolCall":{"id":"call_9","name":"invoke_subagent"}}}}`), 0o600)
	if got := ChildrenAtWork(dir, sid, []string{"call_9"}); len(got) != 1 || got[0] != "call_9" {
		t.Fatalf("at work = %v", got)
	}
	// Its report in the parent's inbox ends it.
	write(t, dir, "messages/r.json", `{"sender":"child-1"}`)
	if got := ChildrenAtWork(dir, sid, []string{"call_9"}); len(got) != 0 {
		t.Fatalf("reported child at work: %v", got)
	}
	// So does a kill mark.
	os.Remove(filepath.Join(dir, filepath.Join(brain(sid)...), "messages", "r.json"))
	write(t, dir, "subagents/"+child+".json", `{"conversationId":"child-1","state":"SUBAGENT_STATE_KILLED"}`)
	if got := ChildrenAtWork(dir, sid, []string{"call_9"}); len(got) != 0 {
		t.Fatalf("killed child at work: %v", got)
	}
}
