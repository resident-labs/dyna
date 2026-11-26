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

// piBox opens real PiG the way Run does from a hello: adopt the record, plan the open from it, write what the plan changes, open,
// and attach the wrapper.
type piBox struct {
	t        *testing.T
	cfg      pig.Config
	sessions string
	marker   string
	model    *pigtest.Model
}

func newPiBox(t *testing.T, blocks ...string) *piBox {
	t.Helper()
	var replies [][]pigtest.Block
	for _, text := range blocks {
		replies = append(replies, []pigtest.Block{{Text: text}})
	}
	model := pigtest.NewModel(replies...)
	agentDir := pigtest.AgentDir(t, model, "")
	sessions := filepath.Join(agentDir, "sessions")
	return &piBox{t: t, sessions: sessions, marker: filepath.Join(t.TempDir(), "state", "handoff-gen"), model: model,
		cfg: pig.Config{Dir: t.TempDir(), AgentDir: agentDir, SessionDir: sessions, Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "test-model", Thinking: contract.ThinkingLow}}}
}

func (b *piBox) open(h contract.BoxHelloResponse) (*pig.Harness, *handoffStart) {
	b.t.Helper()
	rec, err := adoptHandoff(h, b.marker)
	if err != nil {
		b.t.Fatal(err)
	}
	open, next, err := planOpen(rec, b.sessions, h.Fresh)
	if err != nil {
		b.t.Fatal(err)
	}
	cfg := b.cfg
	cfg.New, cfg.Resume = open.New, open.Resume
	native, err := pig.Open(b.t.Context(), cfg)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Cleanup(func() { native.Close() })
	w := &handoffStart{boxHarness: native, file: native.SessionFile, sessions: b.sessions, path: b.marker, rec: rec}
	if next != rec {
		if err := w.move(next); err != nil {
			b.t.Fatal(err)
		}
	}
	if err := w.attach(b.t.Context()); err != nil {
		b.t.Fatal(err)
	}
	return native, w
}

func (b *piBox) sent(request int) string {
	var all []string
	for _, m := range b.model.Requests()[request].Messages {
		all = append(all, string(m))
	}
	return strings.Join(all, "\n")
}

func turn(delivery contract.DeliverySeq, text string) []contract.TurnInput {
	return []contract.TurnInput{{Delivery: delivery, Text: text}}
}

// Real PiG: a prompt refused before Pi wrote its new session file leaves a carrier on a file that does not exist. The restart opens a
// fresh conversation, and nothing a /clear left behind reaches the model as native context.
func TestReopenAfterARefusalBeforeTheFirstWriteNeverResumesAClearedFile(t *testing.T) {
	b := newPiBox(t, "old reply", "new reply")
	b.cfg.New = true
	old, _ := b.open(contract.BoxHelloResponse{})
	if _, err := old.Start(t.Context(), "old", turn(1, "PRE_CLEAR_PRIVATE"), func() {}); err != nil {
		t.Fatal(err)
	}
	oldPath := old.SessionFile()
	old.Close()
	if err := agy.MarkCleared(b.sessions, ""); err != nil {
		t.Fatal(err)
	}
	target, w := b.open(hello("7"))
	w.boxHarness = &fakePi{refuse: true}
	if _, err := w.Start(t.Context(), "refused", turn(2, "refuses before write"), func() {}); err == nil {
		t.Fatal("did not refuse")
	}
	target.Close()
	b.cfg.New = false
	reopened, w2 := b.open(hello(""))
	if reopened.SessionFile() == oldPath {
		t.Fatal("the reopen continued the older file")
	}
	if !w2.rec.owed() {
		t.Fatalf("the history is not owed again: %+v", w2.rec)
	}
	if _, err := reopened.Start(t.Context(), "next", turn(3, "next question"), func() {}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.sent(1), "PRE_CLEAR_PRIVATE") {
		t.Fatal("a cleared message reached the model as native context")
	}
}

// Real PiG's ledger settles the handoff: owed at the preflight, accepted once Pi wrote the input.
func TestRealPiLedgerSettlesTheHandoff(t *testing.T) {
	b := newPiBox(t, "first reply")
	native, w := b.open(hello("7"))
	if !w.rec.owed() {
		t.Fatalf("%+v", w.rec)
	}
	if _, err := w.Start(t.Context(), "t", turn(1, "hello"), func() {}); err != nil {
		t.Fatal(err)
	}
	if w.rec.Status != handoffAccepted || onDisk(t, b.marker) != w.rec {
		t.Fatalf("memory %+v, disk %+v", w.rec, onDisk(t, b.marker))
	}
	native.Close()
	if _, w2 := b.open(hello("")); w2.rec.owed() {
		t.Fatalf("a restart owes an accepted handoff: %+v", w2.rec)
	}
}

// A handoff delivered, then /clear while idle, then a restart before the next turn (the hello says Fresh), then the fresh turn and
// another restart: the cleared conversation's first message never reaches the model. Both when the delivered record had settled and
// when it was still sending (a restart before the next start observed it).
func TestIdleClearAfterADeliveredHandoffThenRestartsNeverResumesTheClearedFile(t *testing.T) {
	for _, staleRecord := range []bool{false, true} {
		name := "settled"
		if staleRecord {
			name = "still sending"
		}
		t.Run(name, func(t *testing.T) {
			b := newPiBox(t, "initial reply", "fresh reply", "after reopen reply")
			initial, w := b.open(hello("7"))
			if _, err := w.Start(t.Context(), "initial", turn(1, "CLEARED_PREVIOUS_QUESTION"), func() {}); err != nil {
				t.Fatal(err)
			}
			if staleRecord {
				if err := (handoffRecord{Gen: "7", Status: handoffSending, Carrier: 1, File: filepath.Base(initial.SessionFile())}).save(b.marker); err != nil {
					t.Fatal(err)
				}
			}
			initial.Close()
			// The owner cleared the session while idle; the restarted box's hello says so, and the fresh turn clears before it starts.
			_, afterClear := b.open(contract.BoxHelloResponse{Fresh: true})
			if err := afterClear.NewConversation(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := afterClear.Start(t.Context(), "fresh", turn(2, "new conversation"), func() {}); err != nil {
				t.Fatal(err)
			}
			afterClear.Close()
			reopened, _ := b.open(contract.BoxHelloResponse{})
			if _, err := reopened.Start(t.Context(), "reopen", turn(3, "after reopen"), func() {}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(b.sent(2), "CLEARED_PREVIOUS_QUESTION") {
				t.Fatal("a restart resumed the cleared file and sent its first message to the model")
			}
		})
	}
}
