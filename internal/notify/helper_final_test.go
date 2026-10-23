package notify

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/parentoutput"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestServiceHelperFinalResultReachesReviewerWithoutPost(t *testing.T) {
	for _, trigger := range []string{"initial", "silent-trigger", "changed-parent"} {
		t.Run(trigger, func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			parent := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
			helper := e.start(rec, "helper", &parent)
			if trigger == "changed-parent" {
				parent = markService(t, e, e.start(rec, "next-reviewer", nil), "reviewer")
				if _, err := e.mgr.Store.UpdateMeta(helper.ID, func(meta *subagent.Meta) { meta.ParentSession = parent.ID }); err != nil {
					t.Fatal(err)
				}
			}
			const findings = "Finding: the exact-head check is missing in review.go:12."
			if _, err := e.mgr.Store.UpdateTurn(helper.ID, 1, func(turn *subagent.Turn) {
				turn.Result = findings
				if trigger == "silent-trigger" {
					turn.ReportTo = nil
				}
			}); err != nil {
				t.Fatal(err)
			}
			out, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !turnReport(out.Message, room.TurnCompleted) || out.Message.OwnerReport || !reflect.DeepEqual(out.Message.To, []string{parent.Address()}) || out.Message.SessionIDs[parent.Address()] != parent.ID || !strings.Contains(out.Message.Text, findings) {
				t.Fatalf("helper final did not reach its current reviewer: %+v", out.Message)
			}
			if len(out.Deliveries) != 1 || out.Deliveries[0].Delivery != "session-queued" {
				t.Fatalf("reviewer delivery: %+v", out.Deliveries)
			}
			e.wait(parent.ID, 2)
			if !strings.Contains(e.turnInput(parent.ID, 2), findings) {
				t.Fatal("reviewer's next turn did not receive the findings")
			}
			again, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
			if err != nil || again.Message.ID != "" {
				t.Fatalf("helper final was delivered twice: %+v %v", again, err)
			}
		})
	}
}

func TestServiceHelperLongFinalIsBoundedAndRetryIsSafe(t *testing.T) {
	for _, spillFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "spill", true: "spill-failure"}[spillFails], func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			parent := markService(t, e, e.start(rec, "reviewer", nil), "reviewer")
			helper := e.start(rec, "helper", &parent)
			findings := strings.Repeat("f", 17000)
			if _, err := e.mgr.Store.UpdateTurn(helper.ID, 1, func(turn *subagent.Turn) { turn.Result = findings }); err != nil {
				t.Fatal(err)
			}
			dir, err := e.mgr.Store.Dir(helper.ID)
			if err != nil {
				t.Fatal(err)
			}
			outputDir := filepath.Join(dir, "parent-output")
			if spillFails {
				// A regular file blocks the spill for all test users, including root.
				if err := os.WriteFile(outputDir, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !turnReport(out.Message, room.TurnCompleted) || out.Message.OwnerReport || !reflect.DeepEqual(out.Message.To, []string{parent.Address()}) || len([]rune(out.Message.Text)) > parentoutput.InlineCharLimit+200 {
				t.Fatalf("helper report was not bounded for its reviewer: %+v", out.Message)
			}
			if spillFails {
				if !strings.Contains(out.Message.Text, "Could not save full report") || !strings.Contains(out.Message.Text, helper.ID+" --turn 1") {
					t.Fatalf("fallback lost the stored output pointer: %s", out.Message.Text)
				}
				if err := os.Remove(outputDir); err != nil {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(outputDir, "turn-1.txt")
				full, err := os.ReadFile(path)
				if err != nil || string(full) != findings || !strings.Contains(out.Message.Text, path) {
					t.Fatalf("full helper report was not preserved: %v", err)
				}
			}
			stored, err := e.svc.Rooms.Message(rec.ID, out.Message.ID)
			if err != nil || stored.Text != out.Message.Text {
				t.Fatalf("bounded report was not stored: %v", err)
			}
			again, err := e.svc.CompleteSessionTurn(e.ctx, helper.ID, 1)
			if err != nil || again.Message.ID != "" {
				t.Fatalf("retry duplicated the helper report: %+v %v", again, err)
			}
			turn, err := e.mgr.Store.ReadTurn(helper.ID, 1)
			if err != nil || turn.Result != findings || !turn.Reported {
				t.Fatalf("full stored result or report receipt changed: %v", err)
			}
		})
	}
}
