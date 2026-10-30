package roomsvc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/room"
	"dyna-agent/internal/slashcmd"
	"dyna-agent/internal/workflowcatalog"
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

func TestRunSkillExplicitTargetPreservesMentions(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	e.start(rec, "other", nil)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("Review code."), 0600); err != nil {
		t.Fatal(err)
	}
	if err := harnessconfig.NewConfigManager().AddSkill(e.ctx, harnessconfig.ManagedSkill{Name: "review", Source: dir, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"ask @other", "@other check this"} {
		post, err := e.svc.RunCommand(e.ctx, rec.ID, "review", text, "lead")
		if err != nil {
			t.Fatal(err)
		}
		if len(post.Message.To) != 1 || post.Message.To[0] != lead.Address() || post.Message.Text != text {
			t.Fatalf("text %q: wrong message: %+v", text, post.Message)
		}
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

func TestWorkflowCommandListsAndUsesDMTarget(t *testing.T) {
	e := newEnv(t)
	rec, err := e.svc.rooms().Create(room.CreateInput{Name: "workflow", Repo: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	other := e.room("other")
	target := e.start(other, "lead", nil)
	commands, err := e.svc.Commands(e.ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, command := range commands {
		if command.Name == "deep-research" && command.Source == slashcmd.Workflow {
			found = true
			if len(command.Args) != 1 || command.Args[0].Name != "question" || !command.Args[0].Required {
				t.Fatalf("wrong args: %+v", command.Args)
			}
		}
	}
	if !found {
		t.Fatal("bundled workflow missing")
	}
	_, err = e.svc.RunCommand(e.ctx, rec.ID, "deep-research", "question", target.ID)
	if err == nil || !strings.Contains(err.Error(), "result target must belong") {
		t.Fatalf("wrong target error: %v", err)
	}
}

func TestWorkflowInputNamedFields(t *testing.T) {
	workflows, err := workflowcatalog.List(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		if workflow.Name != "audit" {
			continue
		}
		raw, err := workflowInput(workflow, `what="check x=y" paths=["src/a", "src/b"]`)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			What  string   `json:"what"`
			Paths []string `json:"paths"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.What != "check x=y" || len(got.Paths) != 2 {
			t.Fatalf("input %s: %+v, %v", raw, got, err)
		}
		if _, err := workflowInput(workflow, "what=check"); err == nil {
			t.Fatal("missing paths accepted")
		}
		return
	}
	t.Fatal("audit missing")
}

func TestWorkflowInputPositional(t *testing.T) {
	workflows, err := workflowcatalog.List(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		if workflow.Name != "fix-until" {
			continue
		}
		raw, err := workflowInput(workflow, "FOO=bar go test ./...")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.Command != "FOO=bar go test ./..." {
			t.Fatalf("input %s: %+v, %v", raw, got, err)
		}
		if _, err := workflowInput(workflow, ""); err == nil {
			t.Fatal("missing command accepted")
		}
		return
	}
	t.Fatal("fix-until missing")
}

func TestWorkflowInputOptionalDefaults(t *testing.T) {
	for _, args := range [][]plan.WorkflowArg{{{Name: "limit"}}, {{Name: "limit"}, {Name: "mode"}}} {
		raw, err := workflowInput(workflowcatalog.Info{Args: args}, "")
		if err != nil || string(raw) != "{}" {
			t.Fatalf("input = %s, error = %v", raw, err)
		}
	}
}
