package roomsvc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/room"
	"dyna-agent/internal/slashcmd"
)

func TestRunSkillAddressesMentionAndFramesInstruction(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("Review code."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := harnessconfig.NewConfigManager().AddSkill(e.ctx, harnessconfig.ManagedSkill{Name: "review", Source: dir, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	post, err := e.svc.RunCommand(e.ctx, rec.ID, "review", "@lead check this", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(post.Message.To) != 1 || post.Message.To[0] != lead.Address() || post.Message.Text != "check this" {
		t.Fatalf("wrong message: %+v", post.Message)
	}
	frame := post.Message.Frame(room.FrameOptions{Room: rec.Name})
	if !strings.Contains(frame, "Use the review skill: read "+path+" for this message.") {
		t.Fatalf("missing skill instruction: %s", frame)
	}
}

type fakePrompts struct {
	text string
}

func (f *fakePrompts) Prompts(_ context.Context, _ string) ([]slashcmd.Command, error) {
	return []slashcmd.Command{{Name: "server:hello", Source: slashcmd.MCPPrompt}}, nil
}

func (f *fakePrompts) RunPrompt(_ context.Context, _, _, text string) (string, error) {
	f.text = text
	return "  wrapped @other\n", nil
}

func TestRunPromptWithoutArgumentsPreservesOutputAndTarget(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	prompts := &fakePrompts{}
	e.svc.Prompts = prompts
	commands, err := e.svc.Commands(e.ctx, rec.ID)
	if err != nil || len(commands) == 0 || commands[len(commands)-1].Name != "server:hello" {
		t.Fatalf("commands = %+v, error = %v", commands, err)
	}
	post, err := e.svc.RunCommand(e.ctx, rec.ID, "server:hello", "", "lead")
	if err != nil {
		t.Fatal(err)
	}
	if prompts.text != "" || post.Message.Text != "  wrapped @other\n" || post.Message.Prompt == nil || post.Message.Prompt.Server != "server" || post.Message.Prompt.Prompt != "hello" || len(post.Message.To) != 1 || post.Message.To[0] != lead.Address() {
		t.Fatalf("wrong prompt post: %+v, arguments %q", post.Message, prompts.text)
	}
}
