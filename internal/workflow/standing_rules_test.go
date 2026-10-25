package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/harness/fakeworker"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store/storetest"
)

func TestWorkflowWorkerStandingRules(t *testing.T) {
	for _, hasRoom := range []bool{true, false} {
		t.Run(map[bool]string{true: "room", false: "no-room"}[hasRoom], func(t *testing.T) {
			storetest.Isolate(t)
			t.Setenv(runstore.AgentJournalRootEnv, "")
			rec, err := room.DefaultStore().Create(room.CreateInput{Name: "test-room"})
			if err != nil {
				t.Fatal(err)
			}
			for _, rule := range []struct{ by, name, text string }{{"owner", "", "Keep code simple."}, {"session", "team:tester", "Run touched tests."}} {
				if _, err := goals.DefaultStore().AddRule(rec.ID, rule.by, rule.name, rule.text); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := goals.DefaultStore().AddRule(goals.Global, "owner", "", "Write in Central time."); err != nil {
				t.Fatal(err)
			}
			script := `return await agent("task")`
			run, err := runstore.Create("standing rules", script, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer run.Finish("ok", "", nil)
			if hasRoom {
				run.Meta.RoomID = rec.ID
			} else {
				run.Meta.RoomID = ""
			}
			capture := filepath.Join(t.TempDir(), "prompt")
			worker := fakeworker.InstallClaude(t, `printf '%s' "$DYNA_FAKE_PROMPT" > "$CAPTURE.prompt"; printf '%s' "$DYNA_FAKE_INSTRUCTIONS" > "$CAPTURE.instructions"; printf done`)
			worker.Name, worker.Default = "capture", true
			worker.Env = mergedEnv(worker.Env, map[string]string{"CAPTURE": capture})
			store := &profile.Store{Profiles: []profile.Profile{worker}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := Execute(ctx, Options{ScriptSrc: script, Store: store, Run: run, WorkDir: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			prompt, err := os.ReadFile(capture + ".prompt")
			if err != nil {
				t.Fatal(err)
			}
			instructions, err := os.ReadFile(capture + ".instructions")
			if err != nil {
				t.Fatal(err)
			}
			// The rules travel in the harness's own instruction channel, and the
			// prompt carries the task alone.
			if !strings.Contains(string(prompt), "[ORIGINAL TASK]\ntask") || strings.Contains(string(prompt), "[DYNA RULES]") {
				t.Fatalf("room=%v prompt = %q", hasRoom, prompt)
			}
			got := string(instructions)
			// Global rules reach every worker, with or without a room.
			if !strings.Contains(got, "[DYNA GLOBAL RULES]") || !strings.Contains(got, "Write in Central time. · owner") {
				t.Fatalf("room=%v global rule missing: %q", hasRoom, got)
			}
			for _, want := range []string{"[DYNA RULES]", "Keep code simple. · owner", "Run touched tests. · agent team:tester"} {
				if strings.Contains(got, want) != hasRoom {
					t.Fatalf("room=%v missing/unexpected %q: %q", hasRoom, want, got)
				}
			}
		})
	}
}

// Every worker gets the restriction; a journaling worker also gets the
// journal, and room rules join both through the shared sections.
func TestWorkerInstructionsSections(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := goals.DefaultStore().AddRule("test-room", "owner", "", "Keep code simple."); err != nil {
		t.Fatal(err)
	}
	e := &engine{opts: Options{Run: &runstore.Run{Meta: runstore.Meta{RoomID: "test-room"}}}}
	leaf, journal := e.workerInstructions(false), e.workerInstructions(true)
	for name, got := range map[string]string{"leaf": leaf.Text(), "journal": journal.Text()} {
		if !strings.Contains(got, "[DYNA WORKFLOW RESTRICTION]") || strings.Count(got, "[DYNA RULES]") != 1 || !strings.Contains(got, "Keep code simple. · owner") {
			t.Fatalf("%s instructions = %q", name, got)
		}
	}
	if strings.Contains(leaf.Text(), "DYNA WORK JOURNAL") || !strings.HasPrefix(journal.Text(), "[DYNA WORK JOURNAL - REQUIRED]") {
		t.Fatal("the journal section is misplaced")
	}
}
