package computerweb

import (
	"dyna-agent/internal/computers"
	"encoding/json"
	"testing"
)

func TestHandoffReasonStaysInPillWhileAgentWaits(t *testing.T) {
	var state computers.Status
	if err := json.Unmarshal([]byte(`{"control":"human","latest_status":{"text":"Waiting to check the page"},"control_reason":{"text":"Please sign in"}}`), &state); err != nil {
		t.Fatal(err)
	}
	info := FromStatus(state, "worker")
	if info.LatestStatus == nil || info.LatestStatus.Text != "Please sign in" {
		t.Fatalf("handoff reason is hidden: %+v", info.LatestStatus)
	}
	state.Control = computers.ControlAgent
	info = FromStatus(state, "worker")
	if info.LatestStatus.Text != "Waiting to check the page" {
		t.Fatal("agent intent did not return after handoff")
	}
}
