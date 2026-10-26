package workflowsvc

import (
	"context"
	"os"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestLeafSessionExecuteRefusesRunStart(t *testing.T) {
	storetest.Isolate(t)
	id := "ses_20261026-000002-aaaaaaaa"
	meta := subagent.Meta{ID: id, Name: "leaf", Room: "work", RoomID: "room_work", Profile: "mock", Depth: 2, Leaf: true}
	if err := sessionlife.Save(subagent.DefaultStore().Root, id, meta); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, id)
	_, err := Execute(context.Background(), Options{}, nil, Observer{})
	if err == nil || err.Error() != subagent.ErrLeafStart.Error() {
		t.Fatalf("Execute as a leaf session = %v; want %q", err, subagent.ErrLeafStart)
	}
}

func TestLeafSessionLaunchRefusesWithoutSpawning(t *testing.T) {
	storetest.Isolate(t)
	id := "ses_20261026-000003-aaaaaaaa"
	meta := subagent.Meta{ID: id, Name: "leaf", Room: "work", RoomID: "room_work", Profile: "mock", Depth: 2, Leaf: true}
	if err := sessionlife.Save(subagent.DefaultStore().Root, id, meta); err != nil {
		t.Fatal(err)
	}
	t.Setenv(mode.EnvMode, mode.Subagent)
	t.Setenv(mode.EnvSession, id)
	_, err := Launch(context.Background(), Options{Name: "leaf-launch", ScriptSrc: "return 1"}, LaunchOptions{})
	if err == nil || err.Error() != subagent.ErrLeafStart.Error() {
		t.Fatalf("Launch as a leaf session = %v; want %q", err, subagent.ErrLeafStart)
	}
	// Nothing was staged: the refusal comes before the run directory exists.
	if entries, err := os.ReadDir(runstore.RunsDir()); err == nil && len(entries) > 0 {
		t.Fatalf("refused launch still staged %d run directories", len(entries))
	}
}
