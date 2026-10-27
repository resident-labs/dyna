package request

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestAskCLI(t *testing.T) {
	storetest.Isolate(t)
	// Owner questions with no room go to the current repo's room.
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	t.Chdir(repo)

	svc := notify.Default()
	sender := notify.Sender{Kind: notify.SenderOwner}
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "AskQuestion", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, req notify.AskRequest) (questions.Question, error) {
			return svc.Ask(ctx, sender, req.Room, req.To, req.Text, req.Meanwhile, req.Options, req.Files)
		}},
		{Name: "AnswerQuestion", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, ref, id, choice, text string) error {
			return svc.AnswerQuestion(ctx, sender, ref, id, choice, text)
		}},
		{Name: "WithdrawRequest", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(_ context.Context, ref, id, reason string) error {
			return svc.WithdrawQuestion(sender, ref, id, reason)
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	run := func(args ...string) (string, error) {
		c := NewCommand()
		var b bytes.Buffer
		c.SetOut(&b)
		c.SetErr(&b)
		c.SetArgs(args)
		err := c.Execute()
		return b.String(), err
	}
	if _, err := run("question", "Why?"); err == nil || !strings.Contains(err.Error(), "--meanwhile") {
		t.Fatal(err)
	}
	dir := t.TempDir()
	html := filepath.Join(dir, "slice.html")
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(html, []byte("<html><title>Slice</title><body>Test</body></html>"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(file, []byte("notes"), 0600)
	// Keep attachment transport here; visual.Preview owns browser rendering.
	out, err := run("question", "May I?", "--meanwhile", "test a slice", "--options", "yes, no", "--attach", html, "--attach", file)
	if err != nil || !strings.Contains(out, "Asked q1. Keep working;") {
		t.Fatal(out, err)
	}
	s := notify.Default()
	rec, _ := room.DefaultStore().CurrentRepoRoom()
	if rec.Repo == "" {
		t.Fatalf("question room = %+v", rec)
	}
	qs, _ := s.QuestionStore().List(rec.ID)
	if len(qs) != 1 || len(qs[0].Options) != 2 || qs[0].Options[1] != "no" {
		t.Fatal(qs)
	}
	p, err := s.Rooms.Message(rec.ID, qs[0].Post)
	if err != nil || len(p.Attachments) != 2 {
		t.Fatal(p, err)
	}
	// Agents talk with posts. Only the owner gets questions.
	for _, args := range [][]string{{"question", "Helper?", "--meanwhile", "test", "--to", "@helper"}, {"question", "@helper", "Helper?", "--meanwhile", "test"}} {
		if _, err = run(args...); err == nil || !strings.Contains(err.Error(), "Ask agents with dyna post; dyna request question is for the owner.") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err = run("question", "Owner?", "--meanwhile", "test", "--to", "@root"); err != nil {
		t.Fatal(err)
	}
	qs, _ = s.QuestionStore().List(rec.ID)
	if len(qs) != 2 || qs[1].To != "owner" {
		t.Fatal(qs)
	}
	if _, err = run("answer", "q1", "--choice", "yes"); err != nil {
		t.Fatal(err)
	}
	if out, err = run("question"); err != nil || !strings.Contains(out, "answered") {
		t.Fatal(out, err)
	}
	if _, err = run("question", "More?", "--meanwhile", "test", "--options", "a,b,c,d,e,f,g"); err == nil {
		t.Fatal("too many options")
	}
}

func TestDaemonCommandRemoved(t *testing.T) {
	for _, cmd := range NewCommand().Commands() {
		if cmd.Name() == "_deliver" {
			t.Fatalf("daemon command %q remains registered", cmd.Name())
		}
	}
}
