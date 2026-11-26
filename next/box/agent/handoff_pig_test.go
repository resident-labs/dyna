package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// With real PiG: a prompt refused before Pi wrote its new session file leaves a carrier with no file. The reopen, planned as Run plans
// it, starts a fresh conversation, and nothing a /clear left behind reaches the model as native context.
func TestReopenAfterARefusalBeforeTheFirstWriteNeverResumesAClearedFile(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "old conversation response"}}, []pigtest.Block{{Text: "new conversation response"}})
	agentDir := pigtest.AgentDir(t, model, "")
	sessions := filepath.Join(agentDir, "sessions")
	cfg := pig.Config{Dir: t.TempDir(), AgentDir: agentDir, SessionDir: sessions, Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow}, New: true}
	old, err := pig.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Start(t.Context(), "old", []contract.TurnInput{{Delivery: 1, Text: "PRE_CLEAR_PRIVATE"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	oldPath := old.SessionFile()
	old.Close()
	if err := agy.MarkCleared(sessions, ""); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "state.json")
	target, err := pig.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	state, err := loadHandoff(hello("7"), marker)
	if err != nil {
		t.Fatal(err)
	}
	h := &handoffStart{boxHarness: &startRecorder{refuse: true}, file: target.SessionFile, sessions: sessions, history: "VALID_HANDOFF_HISTORY", marker: marker, state: state}
	if _, err := h.Start(t.Context(), "refused", []contract.TurnInput{{Delivery: 2, Text: "refuses before write"}}, func() {}); err == nil {
		t.Fatal("did not refuse")
	}
	target.Close()
	disk, err := loadHandoff(hello(""), marker)
	if err != nil {
		t.Fatal(err)
	}
	newConversation, resume, plan, err := disk.openPlan(sessions, false)
	if err != nil {
		t.Fatal(err)
	}
	if !newConversation || resume != "" || !plan.Pending {
		t.Fatalf("plan: new=%v resume=%q %+v", newConversation, resume, plan)
	}
	cfg.New, cfg.Resume = newConversation, resume
	reopened, err := pig.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.SessionFile() == oldPath {
		t.Fatal("the reopen continued the older file")
	}
	if _, err := reopened.Start(t.Context(), "next", []contract.TurnInput{{Delivery: 3, Text: "next question"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	for _, m := range model.Requests()[1].Messages {
		if strings.Contains(string(m), "PRE_CLEAR_PRIVATE") {
			t.Fatal("a cleared message reached the model as native context")
		}
	}
}
