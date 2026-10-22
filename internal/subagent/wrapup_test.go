package subagent

import (
	"dyna-agent/internal/questions"
	"strings"
	"testing"
	"time"
)

func TestWrapUpRunsOnce(t *testing.T) {
	e := newTestEnv(t)
	waitRunners := e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	waitRunners()
	e.m.WrapUpProviders = []WrapUpProvider{func(Info) []string { return []string{"Close stale questions."} }}
	e.m.wrapUp(e.ctx, info.ID, 1)
	info, turn := e.wait(info.ID, 2)
	waitRunners()
	if !turn.WrapUp || !strings.Contains(turn.Prompt, "[DYNA WRAP-UP]") || len(turn.ReportTo) != 0 {
		t.Fatal(turn)
	}
	e.m.wrapUp(e.ctx, info.ID, 2)
	e.m.wrapUp(e.ctx, info.ID, 1)
	info, _ = e.m.Store.Info(info.ID)
	if info.Turns != 2 {
		t.Fatal(info.Turns)
	}
}
func TestWrapUpSkipsEmptyAndFailed(t *testing.T) {
	e := newTestEnv(t)
	waitRunners := e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	waitRunners()
	e.m.WrapUpProviders = []WrapUpProvider{func(Info) []string { return nil }}
	e.m.wrapUp(e.ctx, info.ID, 1)
	fresh, _ := e.m.Store.Info(info.ID)
	if fresh.Turns != 1 {
		t.Fatal(fresh.Turns)
	}
	e.m.WrapUpProviders = []WrapUpProvider{func(Info) []string { return []string{"Reminder"} }}
	for _, status := range []string{TurnError, TurnStopped} {
		_, _ = e.m.Store.UpdateTurn(info.ID, 1, func(t *Turn) { t.Status = status })
		e.m.wrapUp(e.ctx, info.ID, 1)
		fresh, _ = e.m.Store.Info(info.ID)
		if fresh.Turns != 1 {
			t.Fatal(status, fresh.Turns)
		}
	}
}

func TestQuestionWrapUpWaitsForAnotherTurn(t *testing.T) {
	e := newTestEnv(t)
	waitRunners := e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	waitRunners()
	q, err := e.m.questionStore().Ask(questions.Question{Room: info.RoomID, Asker: info.ID, Text: "Keep this?", Meanwhile: "test", AskedTurn: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lines := e.m.questionWrapUp(info); len(lines) != 0 {
		t.Fatal(lines)
	}
	info.Turns = 2
	lines := e.m.questionWrapUp(info)
	if len(lines) != 1 || !strings.Contains(lines[0], q.ID) || !strings.Contains(lines[0], "dyna ask withdraw") {
		t.Fatal(lines)
	}
	if _, err = e.m.questionStore().Withdraw(info.RoomID, q.ID, info.ID, false, "done"); err != nil {
		t.Fatal(err)
	}
	if lines = e.m.questionWrapUp(info); len(lines) != 0 {
		t.Fatal(lines)
	}
}

func TestAssignSendsFollowUpWithSchema(t *testing.T) {
	e := newTestEnv(t)
	waitRunners := e.joinRunners()
	info := completedFixtureSession(t, e)
	turn, err := e.m.Assign(e.ctx, info.ID, "[ASSIGNMENT a]\nRESPOND: {}", `{"type":"object"}`, time.Time{})
	if err != nil || turn.N != 2 || turn.Prompt != "[ASSIGNMENT a]\nRESPOND: {}" || turn.Schema == "" {
		t.Fatalf("%+v %v", turn, err)
	}
	e.wait(info.ID, 2)
	waitRunners()
}
