package workflowsvc

import (
	"context"
	"testing"

	"dyna-agent/internal/mode"
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
