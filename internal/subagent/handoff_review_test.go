package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/harness"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
)

// Exercise the existing native argument builder with real standing instructions.
// The ProcessRunner replaces only model execution; the harness enforces its
// actual 64 KiB argument limit before calling it.
func assertNativeArgumentsFit(t *testing.T, text, prompt string) {
	t.Helper()
	// Grok runs over ACP: its instructions ride the session's first prompt,
	// not argv, so the 64 KiB argument limit no longer applies to it.
	for _, native := range []string{profile.HarnessCodex} {
		called := false
		p := profile.Profile{Harness: native, RuntimeInstructions: text}
		_, err := harness.RunWithOptions(context.Background(), p, prompt, t.TempDir(), false,
			harness.JournalOptions{}, harness.SteeringOptions{}, harness.SessionOptions{}, harness.ExecutionOptions{
				ProcessRunner: func(_ context.Context, args []string, stdout, _ io.Writer) error {
					called = true
					found := false
					for _, arg := range args {
						if len(arg) > 64*1024 {
							return fmt.Errorf("native argument needs %d bytes, above 64 KiB", len(arg))
						}
						if strings.HasPrefix(arg, "developer_instructions=") {
							var decoded string
							if err := json.Unmarshal([]byte(strings.TrimPrefix(arg, "developer_instructions=")), &decoded); err != nil {
								return err
							}
							found = decoded == text
							t.Logf("%s native instructions argument: %d bytes", native, len(arg))
						} else if arg == text {
							found = true
							t.Logf("%s native instructions argument: %d bytes", native, len(arg))
						}

					}
					if !found {
						return fmt.Errorf("native instructions were not passed intact")
					}
					fmt.Fprintln(stdout, `{"type":"thread.started","thread_id":"source-review"}`)
					fmt.Fprintln(stdout, `{"type":"item.completed","item":{"type":"agent_message","text":"Sources reviewed","phase":"final_answer"}}`)
					fmt.Fprintln(stdout, `{"type":"turn.completed"}`)
					return nil
				},
			})
		if err != nil || !called {
			t.Fatalf("%s rejected native instructions before model execution: %v", native, err)
		}
	}
}

func assertSourceReview(t *testing.T, text, id string) {
	t.Helper()
	for _, want := range []string{"Review this source before acting", "dyna subagent history " + id, "dyna subagent output " + id, "dyna subagent transfer status " + id} {
		if !strings.Contains(text, want) {
			t.Errorf("source review lacks %q", want)
		}
	}
	if strings.Contains(text, "--room") {
		t.Error("source review must use the receiving session's room")
	}
}

func TestConsolidationMaximumSourcesUsesReviewReferences(t *testing.T) {
	e, _ := consolidationEnv(t)
	// Valid maximum global and room rule sets exercise real standing overhead.
	for _, scope := range []string{goals.Global, e.room.ID} {
		for range 6 {
			if _, err := e.m.goalStore().AddRule(scope, "owner", "", strings.Repeat("😀", goals.MaxRuleText)); err != nil {
				t.Fatal(err)
			}
		}
	}
	body := strings.Repeat("large source evidence \\\" 😀\n", 1200)
	handoff := strings.Repeat("written source handoff ", 1300)
	var ids []string
	for i := range 16 {
		info := completedFixtureSession(t, e, body)
		if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) {
			if i > 0 {
				meta.Name = strings.Repeat("source-name", 1000)
			}
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.goalStore().Add(info.RoomID, "owner", goals.Input{Text: fmt.Sprintf("Continue source %d; parser work remains open", i), Holders: []string{info.ID}}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, info.ID)
	}
	pending, err := e.m.rooms().Post(e.room.ID, room.PostInput{From: e.room.Name, To: []string{"source"}, SessionIDs: map[string]string{"source": ids[1]}, Text: "pending source responsibility"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.m.Consolidate(context.Background(), ConsolidationOptions{Sessions: ids, Target: ids[0], Profile: "next"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := e.m.SubmitHandoff(context.Background(), id, consolidationToken(c.ID, id), handoff, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	c = consolidationStep(t, e, c)
	c = consolidationStep(t, e, c)
	if c.Phase != "starting" {
		t.Fatalf("phase = %s", c.Phase)
	}
	meta, err := e.m.Store.ReadMeta(c.Target)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := e.m.consolidationPromptMeta(meta, c)
	if err != nil {
		t.Fatal(err)
	}
	runtime := e.m.desiredInstructions(preview).Text()
	assertNativeArgumentsFit(t, runtime, "Review sources")
	if strings.Contains(runtime, handoff[:1000]) || strings.Contains(runtime, "source-namesource-name") || strings.Contains(runtime, body[:1000]) {
		t.Fatal("source bodies or names leaked into native instructions")
	}
	for _, source := range c.Sources {
		assertSourceReview(t, c.Charter, source.Session)
		// The archived source context is the same lean packet a replacement gets:
		// it carries the start of the written handoff and points at the complete
		// text, which stays in the source's saved transfer record.
		if !strings.Contains(source.Context, handoff[:1000]) || !strings.Contains(source.Context, "truncated") || source.Dir == "" {
			t.Fatal("source context lost its bounded handoff or its task directory")
		}
		// An oversized session name is bounded in the header, so it cannot spend
		// the budget the handoff and the read commands need.
		if strings.Contains(source.Context, strings.Repeat("source-name", 100)) || !strings.Contains(source.Context, "dyna subagent output "+source.Session) {
			t.Fatal("source context kept an oversized name or lost its read commands")
		}
		old, err := e.m.Store.ReadTurn(source.Session, 1)
		if err != nil || old.Prompt != body {
			t.Fatal("source history changed", err)
		}
		state, err := e.m.TransferStatus(source.Session)
		if err != nil || state.WrittenHandoff != handoff {
			t.Fatal("written source handoff changed", err)
		}
	}
	found := false
	for _, in := range preview.InheritedInboxes {
		for _, id := range in.PendingIDs {
			found = found || id == pending.ID
		}
	}
	if !found {
		t.Fatal("pending source responsibility was lost")
	}
}

func TestConsolidationResumeRebuildsSavedOversizedCharter(t *testing.T) {
	e, _ := consolidationEnv(t)
	a := completedFixtureSession(t, e)
	b := completedFixtureSession(t, e)
	c := consolidatePair(t, e, a, b)
	submitAll(t, e, c)
	c = consolidationStep(t, e, c)
	c = consolidationStep(t, e, c)
	saved := c.Sources[0].Context
	oversized := strings.Repeat("old oversized charter body ", 4000)
	if err := e.m.updateConsolidation(c.Target, c.ID, func(state *ConsolidationState) error {
		state.Charter, state.Phase = oversized, "failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.ResumeConsolidation(context.Background(), c.RoomID, c.ID); err != nil {
		t.Fatal(err)
	}
	c = consolidationStep(t, e, c)
	if strings.Contains(c.Charter, "old oversized charter body") {
		t.Fatal("resume reused the oversized saved charter")
	}
	if c.Sources[0].Context != saved {
		t.Fatal("resume replaced archived source context")
	}
	meta, err := e.m.Store.ReadMeta(c.Target)
	if err != nil {
		t.Fatal(err)
	}
	// Also cover engine recovery while startup is already durable.
	c.Charter = oversized
	preview, err := e.m.consolidationPromptMeta(meta, c)
	if err != nil {
		t.Fatal(err)
	}
	assertNativeArgumentsFit(t, e.m.desiredInstructions(preview).Text(), "Review sources")
	assertSourceReview(t, preview.ConsolidationCharter, b.ID)
}

func TestTransferAndRootSeatPreserveWrittenHandoffsAndReviewReferences(t *testing.T) {
	for _, rootSeat := range []bool{false, true} {
		t.Run(fmt.Sprintf("root-seat-%t", rootSeat), func(t *testing.T) {
			e, _ := transferEnv(t)
			info := completedFixtureSession(t, e, "Continue the parser task")
			handoff := "Open parser work. " + strings.Repeat("w", 32*1024-len("Open parser work. "))
			state, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "next", rootSeat: rootSeat})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.m.SubmitHandoff(context.Background(), info.ID, state.ID, handoff, info.Dir, "parser"); err != nil {
				t.Fatal(err)
			}
			advance(t, e, state, 2)
			got, err := e.m.TransferStatus(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertSourceReview(t, got.Handoff, info.ID)
			if !strings.Contains(got.Handoff, "Open parser work.") || got.WrittenHandoff != handoff {
				t.Fatal("handoff lost from the packet or its saved complete text")
			}
			if rootSeat {
				// A root seat handoff is the document that replaces the room's
				// root, so it stays complete under its own larger bound.
				if !strings.Contains(got.Handoff, handoff) {
					t.Fatal("root seat lost the complete written handoff")
				}
			} else if !strings.Contains(got.Handoff, "truncated") {
				t.Fatal("replacement model was not bounded")
			}
			meta, err := e.m.Store.ReadMeta(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertNativeArgumentsFit(t, e.m.desiredInstructions(meta).Text(), got.Handoff)
			if got.Dir != info.Dir {
				t.Fatal("worktree hint changed")
			}
		})
	}
}

func TestAdoptGivesNewParentSourceReviewReferences(t *testing.T) {
	e := newTestEnv(t)
	parent := completedFixtureSession(t, e)
	child := completedFixtureSession(t, e, "Continue parser work. "+strings.Repeat("source body ", 4000))
	if _, err := e.m.Adopt(context.Background(), child.ID, parent.ID); err != nil {
		t.Fatal(err)
	}
	notes := hierarchyNotices(t, e)
	if len(notes) != 1 || notes[0].Wake || len(notes[0].To) != 1 || notes[0].To[0] != parent.Address() {
		t.Fatalf("adopt changed notice routing: %+v", notes)
	}
	assertSourceReview(t, notes[0].Text, child.ID)
	if strings.Contains(notes[0].Text, strings.Repeat("source body ", 100)) {
		t.Fatal("adopt copied source history")
	}
	meta, err := e.m.Store.ReadMeta(child.ID)
	if err != nil || meta.ParentSession != parent.ID || meta.Turns != 1 {
		t.Fatal("adopt changed source history", err)
	}
}

func TestConsolidationCharterStaysStableAcrossSourceTurns(t *testing.T) {
	e, _ := consolidationEnv(t)
	seed := func(turns int) Info {
		t.Helper()
		info := completedFixtureSession(t, e, "Continue the saved parser work")
		turn, err := e.m.Store.ReadTurn(info.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		for n := 2; n <= turns; n++ {
			turn.N = n
			if err := e.m.Store.writeTurn(info.ID, turn); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Turns = turns }); err != nil {
			t.Fatal(err)
		}
		info, err = e.m.Store.Info(info.ID)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	kept, absorbed := seed(5), seed(3)
	c := consolidatePair(t, e, kept, absorbed)
	submitAll(t, e, c)
	c = consolidationStep(t, e, c)
	c = consolidationStep(t, e, c)
	if c.Phase != "starting" {
		t.Fatalf("phase = %s", c.Phase)
	}
	before := e.m.consolidationCharter(c)
	meta, err := e.m.Store.ReadMeta(kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalInstructions := e.m.desiredInstructions(meta)
	for range 2 {
		for _, info := range []Info{kept, absorbed} {
			meta, err := e.m.Store.ReadMeta(info.ID)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := e.m.Store.ReadTurn(info.ID, meta.Turns)
			if err != nil {
				t.Fatal(err)
			}
			turn.N++
			if err := e.m.Store.writeTurn(info.ID, turn); err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.Store.UpdateMeta(info.ID, func(meta *Meta) { meta.Turns++ }); err != nil {
				t.Fatal(err)
			}
		}
		if after := e.m.consolidationCharter(c); after != before {
			t.Fatal("consolidation charter changed after advancing source and kept-session turns")
		}
		current, err := e.m.Store.ReadMeta(kept.ID)
		if err != nil {
			t.Fatal(err)
		}
		if desired := e.m.desiredInstructions(current); desired.Hash != originalInstructions.Hash {
			t.Fatal("source turn changes caused instruction hash drift")
		}
	}
	if strings.Contains(before, "--from-turn") || strings.Contains(before, "--to-turn") {
		t.Fatal("consolidation history references must not contain a live turn range")
	}
	for _, info := range []Info{kept, absorbed} {
		assertSourceReview(t, before, info.ID)
		want := fmt.Sprintf("dyna subagent output %s --turn %d --transcript", info.ID, info.Turns)
		if !strings.Contains(before, want) {
			t.Fatalf("charter lacks saved output reference %q", want)
		}
	}
}
