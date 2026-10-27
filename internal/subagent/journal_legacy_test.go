package subagent

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// A journal line that an older engine wrote still decodes into its typed
// payload, field for field. The journal is append-only, so every line already
// on disk must stay readable: this test writes the exact keys each writer
// produced before the kinds were typed, and one kind this version cannot know.
func TestLegacyJournalLinesDecode(t *testing.T) {
	s, id := turnStore(t)
	legacy := map[JournalKind]string{
		JournalSessionStart:         `{"t":"session_start","profile":"deepseek","depth":1,"parent":"","owner":"root","name":"lead","room":"work","address":"work:lead","dir":"/tmp/s","cwd":"/tmp"}`,
		JournalSessionClosed:        `{"t":"session_closed","by":"agent:lead","handoff":["child handed to parent"]}`,
		JournalSessionReopened:      `{"t":"session_reopened"}`,
		JournalSessionDormant:       `{"t":"session_dormant"}`,
		JournalSessionPromoted:      `{"t":"session_promoted","previousParent":"","previousDepth":2,"parent":"ses_a","depth":1}`,
		JournalSessionReparented:    `{"t":"session_reparented","previousParent":"ses_a","previousDepth":2,"parent":"ses_b","depth":2}`,
		JournalTurnStart:            `{"t":"turn_start","turn":1}`,
		JournalTurnEnd:              `{"t":"turn_end","turn":1,"status":"ok","error":"","writeError":"engine spooled","preview":"done"}`,
		JournalTurnRepaired:         `{"t":"turn_repaired","turn":1,"readStatus":"running","readRevision":"7","gatePid":42,"gateTurn":1,"gateActive":true,"pid":4243,"process":"dyna subagent","caller":"engine"}`,
		JournalRoomDrained:          `{"t":"room_drained","turn":2,"message":"msg_1"}`,
		JournalSteerSubmitted:       `{"t":"steer_submitted","turn":2,"queued":true}`,
		JournalSteerDelivered:       `{"t":"steer_delivered","turn":2}`,
		JournalDefer:                `{"t":"defer","turn":2,"kind":"steer","outcome":"deferred","waitedMs":5000}`,
		JournalModelFallback:        `{"t":"model_fallback","turn":2,"from":"a","to":"b","message":"rate limited"}`,
		JournalHarnessSession:       `{"t":"harness_session","turn":2,"session":"h-9"}`,
		JournalCompactFailed:        `{"t":"compact_failed","turn":2,"message":"no room"}`,
		JournalRuntimeRepin:         `{"t":"runtime_repin","message":"the bundle was removed","version":"1.4.0"}`,
		JournalTurnEmpty:            `{"t":"turn_empty","turn":3}`,
		JournalForgedDelivery:       `{"t":"forged_delivery","turn":3}`,
		JournalWorktreeReportFailed: `{"t":"worktree_cleanup_report_failed","error":"room refused"}`,
		JournalCascadeCloseFailed:   `{"t":"cascade_close_failed","parent":"ses_p","error":"busy"}`,
		JournalChildHandoverFailed:  `{"t":"child_handover_failed","error":"no heir"}`,
	}
	kinds := []JournalKind{
		JournalSessionStart, JournalSessionClosed, JournalSessionReopened, JournalSessionDormant,
		JournalSessionPromoted, JournalSessionReparented, JournalTurnStart, JournalTurnEnd,
		JournalTurnRepaired, JournalRoomDrained, JournalSteerSubmitted, JournalSteerDelivered,
		JournalDefer, JournalModelFallback, JournalHarnessSession, JournalCompactFailed,
		JournalRuntimeRepin, JournalTurnEmpty, JournalForgedDelivery, JournalWorktreeReportFailed, JournalCascadeCloseFailed,
		JournalChildHandoverFailed,
	}
	want := []JournalPayload{
		&SessionStartJournal{Profile: "deepseek", Depth: 1, Owner: "root", Name: "lead", Room: "work", Address: "work:lead", Dir: "/tmp/s", Cwd: "/tmp"},
		&SessionClosedJournal{By: "agent:lead", Handoff: []string{"child handed to parent"}},
		&SessionReopenedJournal{},
		&SessionDormantJournal{},
		&SessionPromotedJournal{SessionMovedJournal{PreviousDepth: 2, Parent: "ses_a", Depth: 1}},
		&SessionReparentedJournal{SessionMovedJournal{PreviousParent: "ses_a", PreviousDepth: 2, Parent: "ses_b", Depth: 2}},
		&TurnStartJournal{Turn: 1},
		&TurnEndJournal{Turn: 1, Status: "ok", WriteError: "engine spooled", Preview: "done"},
		&TurnRepairedJournal{Turn: 1, ReadStatus: "running", ReadRevision: "7", GatePid: 42, GateTurn: 1, GateActive: true, Pid: 4243, Process: "dyna subagent", Caller: "engine"},
		&RoomDrainedJournal{Turn: 2, Message: "msg_1"},
		&SteerSubmittedJournal{Turn: 2, Queued: true},
		&SteerDeliveredJournal{Turn: 2},
		&DeferJournal{Turn: 2, Kind: "steer", Outcome: "deferred", WaitedMs: 5000},
		&ModelFallbackJournal{Turn: 2, From: "a", To: "b", Message: "rate limited"},
		&HarnessSessionJournal{Turn: 2, Session: "h-9"},
		&CompactFailedJournal{Turn: 2, Message: "no room"},
		&RuntimeRepinJournal{Message: "the bundle was removed", Version: "1.4.0"},
		&TurnEmptyJournal{Turn: 3},
		&ForgedDeliveryJournal{Turn: 3},
		&WorktreeReportFailedJournal{Error: "room refused"},
		&CascadeCloseFailedJournal{Parent: "ses_p", Error: "busy"},
		&ChildHandoverFailedJournal{Error: "no heir"},
	}
	unknown := `{"t":"a_kind_from_a_newer_engine","answer":42}`
	lines := make([]string, 0, len(kinds)+1)
	for _, kind := range kinds {
		lines = append(lines, legacy[kind])
	}
	lines = append(lines, unknown)
	path, err := s.EventsPath(id)
	if err != nil {
		t.Fatal(err)
	}
	// The first line carries a stamp, as every appended line does.
	stamped := `{"t":"turn_start","turn":1,"ts":1730000000000}`
	if err := os.WriteFile(path, []byte(strings.Join(append([]string{stamped}, lines...), "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(lines)+1 {
		t.Fatalf("read %d lines of %d", len(events), len(lines)+1)
	}
	if events[0].TS != 1730000000000 {
		t.Errorf("stamp = %d; want the stored one", events[0].TS)
	}
	got := make([]JournalPayload, 0, len(events))
	for _, ev := range events[1:] {
		got = append(got, ev.Payload)
	}
	if len(got) != len(want)+1 {
		t.Fatalf("payloads = %d; want %d", len(got), len(want)+1)
	}
	for i, payload := range want {
		if !reflect.DeepEqual(got[i], payload) {
			t.Errorf("%s decoded to %+v; want %+v", kinds[i], got[i], payload)
		}
	}
	kept, ok := got[len(got)-1].(*UnknownJournal)
	if !ok || kept.Kind != "a_kind_from_a_newer_engine" || string(kept.Raw) != unknown {
		t.Errorf("unknown line = %+v; want the line kept whole", got[len(got)-1])
	}
}

// A line keeps the flat shape the journal has always written, so a reader that
// is not this engine still finds the kind under "t" and the stamp under "ts".
func TestJournalLineKeepsTheStoredShape(t *testing.T) {
	checks := []struct {
		name string
		ev   JournalEvent
		want string
	}{
		{"payload fields", JournalEvent{TS: 7, Payload: TurnStartJournal{Turn: 2}}, `{"t":"turn_start","turn":2,"ts":7}`},
		{"no payload fields", JournalEvent{TS: 7, Payload: SessionDormantJournal{}}, `{"t":"session_dormant","ts":7}`},
		{"unknown kind", JournalEvent{TS: 7, Payload: &UnknownJournal{Kind: "future", Raw: json.RawMessage(`{"t":"future","answer":42}`)}}, `{"t":"future","answer":42}`},
	}
	for _, check := range checks {
		b, err := json.Marshal(check.ev)
		if err != nil {
			t.Fatalf("%s: %v", check.name, err)
		}
		if string(b) != check.want {
			t.Errorf("%s = %s; want %s", check.name, b, check.want)
		}
	}
}
