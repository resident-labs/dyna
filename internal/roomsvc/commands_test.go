package roomsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/room"
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
