package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/agy"
)

func conversation(t *testing.T, dir, name string, age time.Duration, question string) {
	t.Helper()
	line := fmt.Sprintf(`{"type":"message","id":%q,"message":{"role":"user","deliveryId":"1","content":[{"type":"text","text":%q}]}}`, name, question)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// The history reads only conversation files, oldest first, so agy's own record and a stray file never reach the prompt.
func TestHandoffHistoryReadsConversationFilesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "b.jsonl", time.Hour, "second question")
	conversation(t, dir, "a.jsonl", 2*time.Hour, "first question")
	if err := os.WriteFile(filepath.Join(dir, "agy-session.json"), []byte(`{"sessionId":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := handoffHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first, second := strings.Index(got, "first question"), strings.Index(got, "second question"); first < 0 || second < first {
		t.Fatalf("history is not oldest first:\n%s", got)
	}
}

func hello(gen string) contract.BoxHelloResponse {
	return contract.BoxHelloResponse{Handoff: gen != "", HandoffGen: gen}
}

type startRecorder struct {
	boxHarness
	ledger  map[contract.DeliverySeq]bool // the inputs Pi's session file records
	texts   []string
	refuse  bool
	cleared int
}

func (r *startRecorder) Start(_ context.Context, _ contract.TurnID, inputs []contract.TurnInput, accepted func()) (contract.TurnEnded, error) {
	r.texts = append(r.texts, inputs[0].Text)
	if r.refuse {
		return contract.TurnEnded{}, errors.New("refused before the prompt was accepted")
	}
	accepted()
	return contract.TurnEnded{}, nil
}

func (r *startRecorder) Delivered(_ context.Context, seq contract.DeliverySeq) (bool, error) {
	return r.ledger[seq], nil
}

func (r *startRecorder) NewConversation(context.Context) error {
	r.cleared++
	return nil
}

func savedState(t *testing.T, path string) handoffState {
	t.Helper()
	var state handoffState
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func pending(t *testing.T, rec boxHarness, marker, history string) *handoffStart {
	t.Helper()
	state, err := loadHandoff(hello("7"), marker)
	if err != nil {
		t.Fatal(err)
	}
	return &handoffStart{boxHarness: rec, file: func() string { return "" }, sessions: t.TempDir(), history: history, marker: marker, state: state}
}

// A new generation is written as pending as soon as the hello is read, before Pi opens; a later hello that carries no handoff (the
// engine stops sending it after the first turn output, even a rejected one) still finds it owed.
func TestLoadHandoffKeepsAPendingGenerationOverAHelloWithoutOne(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "state.json")
	if state, err := loadHandoff(hello("7"), marker); err != nil || !state.Pending || state.Gen != "7" {
		t.Fatalf("state %+v: %v", state, err)
	}
	if state, err := loadHandoff(hello(""), marker); err != nil || !state.Pending {
		t.Fatalf("a hello with no handoff lost the pending one: %+v: %v", state, err)
	}
	if state, err := loadHandoff(hello("9"), marker); err != nil || state.Gen != "9" || !state.Pending {
		t.Fatalf("a new generation is not pending: %+v: %v", state, err)
	}
	other := filepath.Join(t.TempDir(), "none.json")
	if state, err := loadHandoff(hello(""), other); err != nil || state.Pending {
		t.Fatalf("no handoff and no record is pending: %+v: %v", state, err)
	}
}

// The first start that reaches the harness carries the history and records its input as the carrier; the generation is no longer
// pending in the record, and the next start carries nothing. A refused start before the harness ran leaves nothing recorded as done.
func TestHandoffStartRecordsTheCarrierAndCarriesOnce(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "state.json")
	rec := &startRecorder{}
	h := pending(t, rec, marker, "HISTORY")
	inputs := []contract.TurnInput{{Delivery: 41, Text: "hello"}}
	if _, err := h.Start(t.Context(), "t", inputs, func() {}); err != nil {
		t.Fatal(err)
	}
	if state := savedState(t, marker); state.Gen != "7" || state.Pending || state.Carrier != 41 {
		t.Fatalf("state %+v", state)
	}
	rec.ledger = map[contract.DeliverySeq]bool{41: true}
	if _, err := h.Start(t.Context(), "t", inputs, func() {}); err != nil {
		t.Fatal(err)
	}
	if len(rec.texts) != 2 || !strings.Contains(rec.texts[0], "HISTORY") || !strings.Contains(rec.texts[0], "hello") || rec.texts[1] != "hello" {
		t.Fatalf("prompts %q", rec.texts)
	}
	if inputs[0].Text != "hello" {
		t.Fatal("the caller's input was changed")
	}
	// The second start saw the carrier delivered and settled the handoff.
	if state := savedState(t, marker); state.Carrier != 0 {
		t.Fatalf("settled state %+v", state)
	}
}

// When the record cannot be written, the start fails before the prompt runs, so the history is not carried unrecorded.
func TestHandoffStartFailsWhenItCannotRecordTheCarrier(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "state.json")
	rec := &startRecorder{}
	h := pending(t, rec, marker, "HISTORY")
	h.marker = filepath.Join(t.TempDir(), "missing", "state.json")
	if _, err := h.Start(t.Context(), "t", []contract.TurnInput{{Delivery: 1, Text: "hi"}}, func() {}); err == nil || len(rec.texts) != 0 {
		t.Fatalf("err %v, prompts %q", err, rec.texts)
	}
}

// A /clear before the first prompt drops the pending history and records the generation alone, so a reload does not bring it back.
func TestClearDropsThePendingHandoff(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "state.json")
	rec := &startRecorder{}
	h := pending(t, rec, marker, "HISTORY")
	if err := h.NewConversation(t.Context()); err != nil || rec.cleared != 1 {
		t.Fatalf("cleared %d: %v", rec.cleared, err)
	}
	if _, err := h.Start(t.Context(), "t", []contract.TurnInput{{Delivery: 2, Text: "after the clear"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	if rec.texts[0] != "after the clear" {
		t.Fatalf("the cleared history was sent: %q", rec.texts[0])
	}
	if state := savedState(t, marker); state.Gen != "7" || state.Pending || state.Carrier != 0 {
		t.Fatalf("state %+v", state)
	}
	if state, err := loadHandoff(hello("7"), marker); err != nil || state.Pending {
		t.Fatalf("a reload owes the cleared history: %+v: %v", state, err)
	}
}

// A /clear that cannot record its generation fails, so a reload never finds the cleared history due again after a /clear that reported success.
func TestClearFailsWhenItCannotRecordTheGeneration(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "state.json")
	h := pending(t, &startRecorder{}, marker, "HISTORY")
	h.marker = filepath.Join(t.TempDir(), "missing", "state.json")
	if err := h.NewConversation(t.Context()); err == nil {
		t.Fatal("the /clear reported success without recording it")
	}
}

// Pi's /clear leaves the earlier files behind in the record agy's /clear uses, so a switch to agy carries none of their text.
func TestPiClearThenAHandoffCarriesNothingBeforeIt(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "old-pi.jsonl", 3*time.Hour, "question before the pi clear")
	conversation(t, dir, "new-pi.jsonl", 2*time.Hour, "question after the pi clear")
	h := &handoffStart{boxHarness: &startRecorder{}, file: func() string { return filepath.Join(dir, "new-pi.jsonl") }, sessions: dir}
	if err := h.NewConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := handoffHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "after the pi clear") || strings.Contains(got, "before the pi clear") {
		t.Fatalf("history:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "old-pi.jsonl")); err != nil {
		t.Fatal("a transcript was deleted")
	}
}

// agy's /clear (its harness records the files it leaves behind) then a switch to Pi carries none of them, and a Pi message written
// after the clear survives whatever its file's modification time.
func TestAgyClearBoundaryKeepsPostClearPiMessages(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "pi-before.jsonl", 5*time.Hour, "pi question before the clear")
	conversation(t, dir, "agy-1.jsonl", 4*time.Hour, "agy question before the clear")
	conversation(t, dir, "agy-2.jsonl", time.Hour, "agy question after the clear")
	// A Pi file from after the clear whose modification time is older than the mirror's last write.
	conversation(t, dir, "pi-after.jsonl", 2*time.Hour, "pi question after the clear")
	if err := agy.MarkCleared(dir, "agy-2.jsonl"); err != nil {
		t.Fatal(err)
	}
	// MarkCleared left behind every file but agy-2; the Pi file written after the clear is a new file and not in the record.
	if err := os.WriteFile(filepath.Join(dir, "agy-session.json"), []byte(`{"cleared":["pi-before.jsonl","agy-1.jsonl"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := handoffHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"agy question after the clear", "pi question after the clear"} {
		if !strings.Contains(got, want) {
			t.Fatalf("history lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "before the clear") {
		t.Fatalf("history carries text from before the clear:\n%s", got)
	}
}

// When Pi rejects the carrying prompt before it is delivered, the next start in the same box carries the history again, and once the
// ledger holds the carrier the handoff settles.
func TestHandoffCarriedAgainAfterAPromptPiNeverRecorded(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "state.json")
	rec := &startRecorder{refuse: true}
	h := pending(t, rec, marker, "HISTORY")
	if _, err := h.Start(t.Context(), "t1", []contract.TurnInput{{Delivery: 5, Text: "first"}}, func() {}); err == nil {
		t.Fatal("the refusal was swallowed")
	}
	rec.refuse = false
	if _, err := h.Start(t.Context(), "t2", []contract.TurnInput{{Delivery: 6, Text: "second"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	if len(rec.texts) != 2 || !strings.Contains(rec.texts[0], "HISTORY") || !strings.Contains(rec.texts[1], "HISTORY") || !strings.Contains(rec.texts[1], "second") {
		t.Fatalf("prompts %q", rec.texts)
	}
	if state := savedState(t, marker); state.Carrier != 6 {
		t.Fatalf("the carrier is %d, want the input that carried it last", state.Carrier)
	}
}

// After a delivered handoff, a successful /clear retires the old carrier: a restart onto the new file must not find an input its ledger
// cannot see and reopen a settled generation.
func TestClearRetiresTheCarrierOfADeliveredHandoff(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "state.json")
	rec := &startRecorder{}
	h := pending(t, rec, marker, "HISTORY")
	if _, err := h.Start(t.Context(), "t", []contract.TurnInput{{Delivery: 41, Text: "hi"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	rec.ledger = map[contract.DeliverySeq]bool{41: true}
	if err := h.NewConversation(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state := savedState(t, marker); state.Gen != "7" || state.Pending || state.Carrier != 0 {
		t.Fatalf("state %+v", state)
	}
	if _, err := h.Start(t.Context(), "t2", []contract.TurnInput{{Delivery: 42, Text: "after"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	if rec.texts[1] != "after" {
		t.Fatalf("the history was carried again after the clear: %q", rec.texts[1])
	}
}

// An unreadable record of what a /clear left behind fails the history, rather than sending what the clear dropped.
func TestHandoffHistoryFailsOnAnUnreadableClearRecord(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "pi.jsonl", time.Hour, "an old question")
	if err := os.WriteFile(filepath.Join(dir, "agy-session.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := handoffHistory(dir); err == nil {
		t.Fatalf("history sent with no clear boundary:\n%s", got)
	}
}

// After a restart the history is owed again when the input that carried it is not in Pi's ledger, and not when it is.
func TestHandoffOwedFollowsTheCarriersDelivery(t *testing.T) {
	ledger := map[contract.DeliverySeq]bool{41: true}
	delivered := func(seq contract.DeliverySeq) (bool, error) { return ledger[seq], nil }
	for _, c := range []struct {
		name  string
		state handoffState
		owed  bool
	}{
		{"never carried", handoffState{Gen: "7", Pending: true}, true},
		{"carried and delivered", handoffState{Gen: "7", Carrier: 41}, false},
		{"carried, never recorded", handoffState{Gen: "7", Carrier: 42}, true},
		{"cleared or empty", handoffState{Gen: "7"}, false},
	} {
		if got, err := c.state.owed(delivered); err != nil || got != c.owed {
			t.Errorf("%s: owed %v, %v", c.name, got, err)
		}
	}
}
