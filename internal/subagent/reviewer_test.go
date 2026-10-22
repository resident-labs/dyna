package subagent

import (
	"context"
	"dyna-agent/internal/mode"
	"strings"
	"testing"
)

func TestReviewerHelperLimits(t *testing.T) {
	reviewer := mockProfile("review")
	reviewer.Cost = 8
	cheap := mockProfile("cheap")
	cheap.Cost = 9
	costly := mockProfile("costly")
	costly.Cost = 7
	e := newTestEnv(t, reviewer, cheap, costly)
	e.joinRunners()
	root, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Role: "reviewer", Name: "reviewer", Profile: "review", Dir: t.TempDir(), Task: "SLEEP:1 RESPOND: ready"})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(root.ID, 1)
	stamp := func(id string) {
		t.Setenv(mode.EnvMode, mode.Subagent)
		t.Setenv(mode.EnvSession, id)
		t.Setenv(mode.EnvDepth, "1")
	}
	stamp(root.ID)
	start := func(name string) (Info, error) {
		info, _, err := e.m.Start(context.Background(), StartOptions{Goal: "Finish the test task", Profile: name, Dir: t.TempDir(), Task: "SLEEP:1 RESPOND: helper"})
		if err == nil {
			e.wait(info.ID, 1)
		}
		return info, err
	}
	if _, err := start("costly"); err == nil || !strings.Contains(err.Error(), "cheaper") {
		t.Fatalf("expensive helper: %v", err)
	}
	child, err := start("cheap")
	if err != nil {
		t.Fatal(err)
	}
	stamp(child.ID)
	if _, err := start("cheap"); err == nil || !strings.Contains(err.Error(), "one level") {
		t.Fatalf("nested helper: %v", err)
	}
	stamp(root.ID)
	if _, err := start("review"); err != nil {
		t.Fatal(err)
	}
	if _, err := start("cheap"); err != nil {
		t.Fatal(err)
	}
	if _, err := start("cheap"); err == nil || !strings.Contains(err.Error(), "3 live helpers") {
		t.Fatalf("crew cap: %v", err)
	}
}

func TestReviewerFreshTransferStartsAnotherConversation(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	info, _ := e.start("SLEEP:1 RESPOND: ready")
	e.wait(info.ID, 1)
	_, err := e.m.Store.UpdateMeta(info.ID, func(m *Meta) {
		m.Transfer = &TransferState{ID: "old-transfer", Phase: "completed", Profile: "mock", Model: m.Model}
	})
	if err != nil {
		t.Fatal(err)
	}
	e.m.TransferLaunch = func(*Manager, string, string) error { return nil }
	next, err := e.m.TransferContext(context.Background(), info.ID, TransferOptions{Profile: "mock", Fresh: true})
	if err != nil || next.ID == "old-transfer" {
		t.Fatalf("fresh context: %+v %v", next, err)
	}
}
