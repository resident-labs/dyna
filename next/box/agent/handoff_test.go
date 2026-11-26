package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

func hello(gen string) contract.BoxHelloResponse {
	return contract.BoxHelloResponse{Handoff: gen != "", HandoffGen: gen}
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

// Pi's /clear leaves the earlier files behind in the record agy's /clear uses, so a switch to agy carries none of their text.
func TestPiClearThenAHandoffCarriesNothingBeforeIt(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "old-pi.jsonl", 3*time.Hour, "question before the pi clear")
	conversation(t, dir, "new-pi.jsonl", 2*time.Hour, "question after the pi clear")
	h := &handoffStart{boxHarness: &fakePi{}, file: func() string { return filepath.Join(dir, "new-pi.jsonl") }, sessions: dir, path: filepath.Join(t.TempDir(), "rec")}
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

// agy's /clear (its harness records the files it leaves behind) then a switch to Pi carries none of them, and a Pi message written after
// the clear survives whatever its file's modification time.
func TestAgyClearBoundaryKeepsPostClearPiMessages(t *testing.T) {
	dir := t.TempDir()
	conversation(t, dir, "pi-before.jsonl", 5*time.Hour, "pi question before the clear")
	conversation(t, dir, "agy-1.jsonl", 4*time.Hour, "agy question before the clear")
	conversation(t, dir, "agy-2.jsonl", time.Hour, "agy question after the clear")
	if err := agy.MarkCleared(dir, "agy-2.jsonl"); err != nil {
		t.Fatal(err)
	}
	// A Pi file from after the clear, older by modification time than the mirror's last write.
	conversation(t, dir, "pi-after.jsonl", 2*time.Hour, "pi question after the clear")
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

// fakePi stands in for Pi where no file or ledger is in play: its ledger is a set it fills when told to write.
type fakePi struct {
	boxHarness
	texts  []string
	refuse bool                          // Start fails before the prompt is written
	writes bool                          // Start records its first input in the ledger
	ledger map[contract.DeliverySeq]bool // the inputs the session file records
}

func (f *fakePi) Start(_ context.Context, _ contract.TurnID, inputs []contract.TurnInput, accepted func()) (contract.TurnEnded, error) {
	f.texts = append(f.texts, inputs[0].Text)
	if f.refuse {
		return contract.TurnEnded{}, errors.New("refused before the prompt was written")
	}
	if f.writes {
		if f.ledger == nil {
			f.ledger = map[contract.DeliverySeq]bool{}
		}
		f.ledger[inputs[0].Delivery] = true
	}
	accepted()
	return contract.TurnEnded{}, nil
}

func (f *fakePi) Delivered(_ context.Context, seq contract.DeliverySeq) (bool, error) {
	return f.ledger[seq], nil
}

func (f *fakePi) NewConversation(context.Context) error { return nil }

// onDisk is the record a restart would read.
func onDisk(t *testing.T, path string) handoffRecord {
	t.Helper()
	rec, err := adoptHandoff(hello(""), path)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// start is a handoffStart over fake whose record at path begins as rec.
func start(t *testing.T, fake *fakePi, rec handoffRecord) *handoffStart {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "handoff-gen")
	if rec.Gen != "" {
		if err := rec.save(path); err != nil {
			t.Fatal(err)
		}
	}
	return &handoffStart{boxHarness: fake, file: func() string { return "/sessions/new.jsonl" }, sessions: t.TempDir(), path: path, rec: rec, history: "HISTORY"}
}

// The states x events table of Pi's handoff record. Every cell checks that what a restart would read is what this process holds, so no
// event edits the record in memory alone.
func TestHandoffRecordStatesAndEvents(t *testing.T) {
	statuses := []string{handoffPending, handoffSending, handoffAccepted, handoffCleared}
	sending := handoffRecord{Gen: "7", Status: handoffSending, Carrier: 41, File: "target.jsonl"}
	recordOf := func(status string) handoffRecord {
		if status == handoffSending {
			return sending
		}
		return handoffRecord{Gen: "7", Status: status}
	}
	input := func(delivery contract.DeliverySeq, text string) []contract.TurnInput {
		return []contract.TurnInput{{Delivery: delivery, Text: text}}
	}
	for _, status := range statuses {
		owed := status == handoffPending || status == handoffSending
		// start: an owed history rides on the prompt; the carrier is recorded before the prompt runs
		t.Run(status+"/start", func(t *testing.T) {
			fake := &fakePi{}
			h := start(t, fake, recordOf(status))
			if _, err := h.Start(t.Context(), "t", input(42, "hello"), func() {}); err != nil {
				t.Fatal(err)
			}
			if carried := strings.Contains(fake.texts[0], "HISTORY"); carried != owed {
				t.Fatalf("history carried %v, want %v: %q", carried, owed, fake.texts[0])
			}
			want := recordOf(status)
			if owed {
				want = handoffRecord{Gen: "7", Status: handoffSending, Carrier: 42, File: "new.jsonl"}
			}
			if h.rec != want || onDisk(t, h.path) != want {
				t.Fatalf("memory %+v, disk %+v, want %+v", h.rec, onDisk(t, h.path), want)
			}
		})
		// start with the ledger showing the carrier: sending settles and carries nothing
		t.Run(status+"/start after the ledger shows the carrier", func(t *testing.T) {
			fake := &fakePi{ledger: map[contract.DeliverySeq]bool{41: true}}
			h := start(t, fake, recordOf(status))
			if _, err := h.Start(t.Context(), "t", input(42, "hello"), func() {}); err != nil {
				t.Fatal(err)
			}
			wantCarried := status == handoffPending
			if carried := strings.Contains(fake.texts[0], "HISTORY"); carried != wantCarried {
				t.Fatalf("history carried %v, want %v", carried, wantCarried)
			}
			if status == handoffSending && (h.rec.Status != handoffAccepted || onDisk(t, h.path) != h.rec) {
				t.Fatalf("memory %+v, disk %+v", h.rec, onDisk(t, h.path))
			}
		})
		// refusal: the prompt was built and recorded, Pi wrote nothing; the next start carries the history again
		t.Run(status+"/refusal then another start", func(t *testing.T) {
			fake := &fakePi{refuse: true}
			h := start(t, fake, recordOf(status))
			if _, err := h.Start(t.Context(), "t1", input(42, "first"), func() {}); err == nil {
				t.Fatal("the refusal was swallowed")
			}
			if onDisk(t, h.path) != h.rec {
				t.Fatalf("memory %+v, disk %+v", h.rec, onDisk(t, h.path))
			}
			fake.refuse = false
			if _, err := h.Start(t.Context(), "t2", input(43, "second"), func() {}); err != nil {
				t.Fatal(err)
			}
			if carried := strings.Contains(fake.texts[1], "HISTORY"); carried != owed {
				t.Fatalf("second start carried %v, want %v", carried, owed)
			}
		})
		// accept: a start Pi writes settles the record as accepted, and a restart sees it
		t.Run(status+"/accept", func(t *testing.T) {
			fake := &fakePi{writes: true}
			h := start(t, fake, recordOf(status))
			if _, err := h.Start(t.Context(), "t", input(42, "hello"), func() {}); err != nil {
				t.Fatal(err)
			}
			want := recordOf(status)
			if owed {
				want = handoffRecord{Gen: "7", Status: handoffAccepted}
			}
			if h.rec != want || onDisk(t, h.path) != want {
				t.Fatalf("memory %+v, disk %+v, want %+v", h.rec, onDisk(t, h.path), want)
			}
		})
		// clear: from any state the record is cleared on disk, whatever this process holds
		t.Run(status+"/clear", func(t *testing.T) {
			h := start(t, &fakePi{}, recordOf(status))
			if err := h.NewConversation(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := handoffRecord{Gen: "7", Status: handoffCleared}
			if h.rec != want || onDisk(t, h.path) != want || h.history != "" {
				t.Fatalf("memory %+v, disk %+v, history %q", h.rec, onDisk(t, h.path), h.history)
			}
			fake := h.boxHarness.(*fakePi)
			if _, err := h.Start(t.Context(), "t", input(44, "after the clear"), func() {}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(fake.texts[0], "HISTORY") {
				t.Fatal("the history was carried after a clear")
			}
		})
		// a clear that cannot be recorded fails
		t.Run(status+"/clear that cannot record", func(t *testing.T) {
			h := start(t, &fakePi{}, recordOf(status))
			h.path = t.TempDir() // a folder: the record cannot replace it
			if err := h.NewConversation(t.Context()); err == nil {
				t.Fatal("the /clear reported success without recording it")
			}
		})
	}
}

// planOpen: the open each state asks for, with or without the owner's clear in the hello, and when the carrier's file is missing or
// was cleared.
func TestHandoffPlanOpen(t *testing.T) {
	sessions := t.TempDir()
	conversation(t, sessions, "old.jsonl", time.Hour, "PRE_CLEAR")
	conversation(t, sessions, "target.jsonl", 2*time.Hour, "carrier file")
	target := filepath.Join(sessions, "target.jsonl")
	sending := func(file string) handoffRecord {
		return handoffRecord{Gen: "7", Status: handoffSending, Carrier: 3, File: file}
	}
	for _, c := range []struct {
		name  string
		rec   handoffRecord
		fresh bool
		want  piOpen
		next  handoffRecord
	}{
		{"pending", handoffRecord{Gen: "7", Status: handoffPending}, false, piOpen{New: true}, handoffRecord{Gen: "7", Status: handoffPending}},
		{"pending with the owner's clear", handoffRecord{Gen: "7", Status: handoffPending}, true, piOpen{New: true}, handoffRecord{Gen: "7", Status: handoffPending}},
		{"sending, file written", sending("target.jsonl"), false, piOpen{Resume: target}, sending("target.jsonl")},
		{"sending, file never written", sending("never.jsonl"), false, piOpen{New: true}, handoffRecord{Gen: "7", Status: handoffPending}},
		{"sending, no file named", sending(""), false, piOpen{New: true}, handoffRecord{Gen: "7", Status: handoffPending}},
		{"sending, owner cleared since", sending("target.jsonl"), true, piOpen{New: true}, handoffRecord{Gen: "7", Status: handoffCleared}},
		{"accepted", handoffRecord{Gen: "7", Status: handoffAccepted}, false, piOpen{}, handoffRecord{Gen: "7", Status: handoffAccepted}},
		{"accepted, owner cleared", handoffRecord{Gen: "7", Status: handoffAccepted}, true, piOpen{New: true}, handoffRecord{Gen: "7", Status: handoffAccepted}},
		{"cleared", handoffRecord{Gen: "7", Status: handoffCleared}, false, piOpen{}, handoffRecord{Gen: "7", Status: handoffCleared}},
		{"no handoff ever", handoffRecord{}, false, piOpen{}, handoffRecord{}},
	} {
		got, next, err := planOpen(c.rec, sessions, c.fresh)
		if err != nil || got != c.want || next != c.next {
			t.Errorf("%s: open %+v next %+v: %v; want %+v %+v", c.name, got, next, err, c.want, c.next)
		}
	}
	// Once a /clear left files behind, a restart never continues one of them.
	if err := agy.MarkCleared(sessions, "target.jsonl"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		rec  handoffRecord
		want piOpen
	}{
		{"cleared set, nothing carried", handoffRecord{Gen: "7", Status: handoffAccepted}, piOpen{Resume: target}},
		{"cleared set, carrier's file cleared", sending("old.jsonl"), piOpen{New: true}},
	} {
		got, _, err := planOpen(c.rec, sessions, false)
		if err != nil || got != c.want {
			t.Errorf("%s: open %+v: %v; want %+v", c.name, got, err, c.want)
		}
	}
}

// A new generation from the hello is recorded as pending before Pi opens; a later hello with no handoff (the engine stops sending it
// after the first turn output, even a rejected one) still finds it owed; a record that cannot be read is an error.
func TestAdoptHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "handoff-gen") // its folder does not exist yet
	if rec, err := adoptHandoff(hello("7"), path); err != nil || rec != (handoffRecord{Gen: "7", Status: handoffPending}) {
		t.Fatalf("%+v: %v", rec, err)
	}
	if rec, err := adoptHandoff(hello(""), path); err != nil || !rec.owed() {
		t.Fatalf("a hello with no handoff lost the pending one: %+v: %v", rec, err)
	}
	if rec, err := adoptHandoff(hello("9"), path); err != nil || rec != (handoffRecord{Gen: "9", Status: handoffPending}) {
		t.Fatalf("a new generation: %+v: %v", rec, err)
	}
	if rec, err := adoptHandoff(hello(""), filepath.Join(t.TempDir(), "none")); err != nil || rec != (handoffRecord{}) {
		t.Fatalf("no handoff and no record: %+v: %v", rec, err)
	}
	folder := t.TempDir()
	if rec, err := adoptHandoff(hello(""), folder); err == nil {
		t.Fatalf("a record that cannot be read became %+v", rec)
	}
}

// A restart reads what each event wrote and arrives at the same plan, for every event.
func TestRestartAfterEachEventPlansFromTheDiskRecord(t *testing.T) {
	fake := &fakePi{refuse: true}
	h := start(t, fake, handoffRecord{Gen: "7", Status: handoffPending})
	sessions := t.TempDir()
	h.sessions = sessions
	if _, err := h.Start(t.Context(), "t", []contract.TurnInput{{Delivery: 5, Text: "x"}}, func() {}); err == nil {
		t.Fatal("no refusal")
	}
	rec, err := adoptHandoff(hello(""), h.path)
	if err != nil {
		t.Fatal(err)
	}
	// The refusal left it sending on a file that was never written: the restart opens fresh and the history is pending again.
	open, next, err := planOpen(rec, sessions, false)
	if err != nil || !open.New || next.Status != handoffPending || !slices.Equal([]string{next.Gen}, []string{"7"}) {
		t.Fatalf("open %+v next %+v: %v", open, next, err)
	}
}
