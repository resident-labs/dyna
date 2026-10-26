package notify

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

const startupTimeoutError = "replacement startup produced no model output within 60 seconds"

// A transfer replacement turn that fails with no model output moves the
// transfer to the written-handoff fallback, so its failure notice must say
// that the fallback is starting instead of asking for manual recovery. The
// fallback's own fresh turn fails the move, so its notice keeps the recovery
// line.
func TestReplacementFailureNoticeNamesTheHandoffFallback(t *testing.T) {
	for _, tc := range []struct {
		name         string
		source       string
		phase        string
		turn         int
		wantFallback bool
	}{
		{name: "native resume failed", source: "native", phase: "starting", turn: 1, wantFallback: true},
		{name: "fallback already building", source: "transcript", phase: "stopping", turn: 0, wantFallback: true},
		{name: "fresh fallback turn failed", source: "transcript", phase: "starting", turn: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			rec := e.room()
			child := e.start(rec, "transferfail", nil)
			// A stalled replacement turn wrote no model events, only its start.
			norm, err := e.mgr.Store.NormalizedPath(child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(norm, []byte("{\"v\":1,\"kind\":\"status\",\"name\":\"session\",\"state\":\"started\"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			token := "fixture-transfer"
			_, err = e.mgr.Store.UpdateMeta(child.ID, func(meta *subagent.Meta) {
				meta.Transfer = &subagent.TransferState{
					ID: token, Phase: tc.phase, Profile: "mock", Model: "mock/model",
					Source: tc.source, Turn: tc.turn, Requested: time.Now(),
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) {
				turn.Status, turn.Error, turn.TransferID = subagent.TurnError, startupTimeoutError, token
			})
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !turnReport(res.Message, room.TurnFailed) || !reflect.DeepEqual(res.Message.To, []string{rec.Name}) {
				t.Fatalf("replacement failure report = %+v", res.Message)
			}
			if strings.Contains(res.Message.Text, "Recover:") == tc.wantFallback {
				t.Fatalf("notice recovery line = %q", res.Message.Text)
			}
			if strings.Contains(res.Message.Text, "written-handoff fallback is starting") != tc.wantFallback {
				t.Fatalf("notice fallback line = %q", res.Message.Text)
			}
		})
	}
}

// The fallback-start notice tells the parent to wait, so the fallback's own
// failure must still report: identical consecutive errors must not suppress
// the final recovery notice that ends the move.
func TestFallbackFailureReportsDespiteDuplicateError(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	worker := e.start(rec, "worker", nil)
	// Turn 2 runs before the transfer state exists, because an active
	// transfer blocks ordinary turns.
	if _, err := e.svc.Post(e.ctx, PostInput{
		Sender:   Sender{Kind: SenderOwner, Room: rec.Name},
		Mentions: []string{"@" + worker.Address()},
		Text:     "SLEEP:0 RESPOND: next",
	}); err != nil {
		t.Fatal(err)
	}
	worker = e.wait(worker.ID, 2)
	// Turn 1: the native replacement turn stalled with no model output.
	norm, err := e.mgr.Store.NormalizedPath(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(norm, []byte("{\"v\":1,\"kind\":\"status\",\"name\":\"session\",\"state\":\"started\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	token := "fixture-transfer"
	_, err = e.mgr.Store.UpdateMeta(worker.ID, func(meta *subagent.Meta) {
		meta.Transfer = &subagent.TransferState{
			ID: token, Phase: "starting", Profile: "mock", Model: "mock/model",
			Source: "native", Turn: 1, Requested: time.Now(),
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.mgr.Store.UpdateTurn(worker.ID, 1, func(turn *subagent.Turn) {
		turn.Status, turn.Error, turn.TransferID = subagent.TurnError, startupTimeoutError, token
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.svc.CompleteSessionTurn(e.ctx, worker.ID, 1)
	if err != nil || !strings.Contains(first.Message.Text, "written-handoff fallback is starting") {
		t.Fatalf("fallback-start notice = %+v: %v", first.Message, err)
	}
	// Turn 2: the fallback's fresh turn failed with the same error, and the
	// transfer itself has failed.
	_, err = e.mgr.Store.UpdateMeta(worker.ID, func(meta *subagent.Meta) {
		meta.Transfer = &subagent.TransferState{
			ID: token, Phase: "failed", Profile: "mock", Model: "mock/model",
			Source: "transcript", Turn: 2, Requested: time.Now(), Error: "replacement startup failed",
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.mgr.Store.UpdateTurn(worker.ID, 2, func(turn *subagent.Turn) {
		turn.Status, turn.Error, turn.TransferID = subagent.TurnError, startupTimeoutError, token
	})
	if err != nil {
		t.Fatal(err)
	}
	final, err := e.svc.CompleteSessionTurn(e.ctx, worker.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !turnReport(final.Message, room.TurnFailed) || !strings.Contains(final.Message.Text, "Recover:") {
		t.Fatalf("final fallback failure report = %+v", final.Message)
	}
}
