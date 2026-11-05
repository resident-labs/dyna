package pig

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

func TestModelInstructionsFollowTransfersAndSurviveSnapshots(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "one"}}, []pigtest.Block{{Text: "two"}}, []pigtest.Block{{Text: "three"}})
	c := config(t, model, "")
	c.ModelInstructions = "Use spaced prose."
	h, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx := context.Background()
	for i, instructions := range []string{"Use spaced prose.", "", "Use spaced prose."} {
		if i > 0 {
			target := c.Model
			if i == 1 {
				target.ID = "small-model"
			}
			if _, err := h.TransferModel(ctx, "turn_b", target, 0, instructions); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.SetPromptSections(ctx, []contract.ContextSection{{Key: contract.ContextGoal, Text: "g1: keep this too"}}); err != nil {
			t.Fatal(err)
		}
		start(t, h, "next")
	}
	requests := model.Requests()
	if !strings.Contains(string(requests[0].System), `Use spaced prose.`) || !strings.Contains(string(requests[0].System), `g1: keep this too`) {
		t.Fatal("first request lost model instructions or snapshot")
	}
	second, third := "", ""
	for _, msg := range requests[1].Messages {
		second += string(msg)
	}
	for _, msg := range requests[2].Messages {
		third += string(msg)
	}
	if !strings.Contains(second, "Removed section model_instructions") {
		t.Fatal("transfer away did not remove model instructions")
	}
	if !strings.Contains(third, `New section model_instructions:\n<model_instructions>\nUse spaced prose.`) {
		t.Fatal("transfer back did not add model instructions")
	}
}

func TestRefusedAndAbortedTransfersKeepModelInstructions(t *testing.T) {
	for _, refused := range []bool{true, false} {
		t.Run(map[bool]string{true: "refused", false: "aborted"}[refused], func(t *testing.T) {
			read := []pigtest.Block{{Text: "read"}}
			model := pigtest.NewModel(read, read, read, read)
			c := config(t, model, "")
			c.ModelInstructions = "Keep spacing."
			h, err := Open(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			longTurns(t, model, h)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target := contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "small-model"}
			if refused {
				target.Provider = "unknown"
			} else {
				cancel()
			}
			result, err := h.TransferModel(ctx, "turn_b", target, 0, "")
			if refused && !errors.Is(err, harness.ErrRefused) {
				t.Fatalf("err %v", err)
			}
			if !refused && (err != nil || result.Path != harness.TransferAborted) {
				t.Fatalf("%+v %v", result, err)
			}
			if err := h.SetPromptSections(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			start(t, h, "still here")
			reqs := model.Requests()
			if !strings.Contains(string(reqs[0].System), "Keep spacing.") {
				t.Fatal("opening model instructions missing")
			}
			for _, msg := range reqs[len(reqs)-1].Messages {
				if strings.Contains(string(msg), "Removed section model_instructions") {
					t.Fatal("failed transfer removed guidance")
				}
			}
		})
	}
}
