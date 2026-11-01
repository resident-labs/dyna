package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract/internal/contractgen"
)

func TestEventRegistry(t *testing.T) {
	bodies := []EventBody{ConnectConnector{Missing: true}, AccountFailed{AccountID: "a", Reason: AccountRefreshFailed}, AccountQuotaLow{AccountID: "a", Window: "week", RemainingPercent: 5, Floor: 10}, RoomLimitsChanged{CPUs: 2, MemBytes: 1024}, RoomPaused{}, RoomResumed{}, RoomDeleting{}, RoomDeleted{}, PostCreated{Text: "hello"}, SessionStarted{Name: "agent"}, SessionRestarted{Session: "ses_a"}, SessionMoved{Session: "ses_a", NewParent: "ses_b"}, SessionClosed{Session: "ses_a"}, TurnStarted{Turn: "turn_a"}, TurnEnded{Final: "done"}, TurnActivity{Label: "read spec.md"}, TranscriptChanged{Session: "ses_a", Turn: "turn_a"}, ConversationRead{Room: "room", Seq: 10}, BoxChanged{To: BoxReady}, ImageBuilt{Host: "host", Repo: "owner/repo", Seconds: 1.5}, ImageFailed{Host: "host", Repo: "owner/repo", Reason: "failed", LogTail: "log"}, OpRequested{ID: "op_a"}, OpDecided{ID: "op_a", State: OpDenied}, OpRan{ID: "op_a", State: OpDone}, PrOpened{Repo: "owner/repo", Number: 1}, ShellJobEnded{Job: "job_1", ExitCode: 1}, BoxPressure{Box: "box_a", Kind: PressureMax}, GrantExpired{Grant: "grant_a"}, ComputerChanged{Session: "ses_a", To: ComputerRunning}, HostDiskLow{Host: "host", FreeBytes: 1, FloorBytes: 2}, SessionProfileChanged{Session: "ses_a", Profile: "gpt"}, QuestionAsked{ID: "q_a", Session: "ses_a", Text: "which?"}, QuestionAnswered{ID: "q_a", Session: "ses_a", Question: "which?", Answer: "this"}, QuestionWithdrawn{ID: "q_a", Session: "ses_a"}, GoalSet{Goal: "g1", Session: "ses_a", Text: "ship"}, GoalItemChanged{Goal: "g1", Item: 1, Text: "test", State: GoalItemDone}, GoalEnded{Goal: "g1", State: GoalDropped, Summary: "moot"}, CheckinDue{Session: "ses_a", Children: []CheckinChild{{Session: "ses_b", Name: "worker", State: CheckinIdle, Goal: "g1", Text: "ship"}}}, SessionFailed{To: "ses_a", Failures: []SessionFailure{{Session: "ses_b", Name: "worker", Box: BoxLost, Reason: "box disconnected"}}}}
	want := []EventType{"connect.connector", "account.failed", "account.quota_low", "room.limits_changed", "room.paused", "room.resumed", "room.deleting", "room.deleted", "post.created", "session.started", "session.restarted", "session.moved", "session.closed", "turn.started", "turn.ended", "turn.activity", "transcript.changed", "conversation.read", "box.changed", "image.built", "image.failed", "op.requested", "op.decided", "op.ran", "pr.opened", "shell.job.ended", "box.pressure", "grant.expired", "computer.changed", "host.disk_low", "session.profile_changed", "question.asked", "question.answered", "question.withdrawn", "goal.set", "goal.item_changed", "goal.ended", "checkin.due", "session.failed"}
	if len(eventBodyTypes) != len(bodies) {
		t.Fatalf("registry has %d entries, want %d", len(eventBodyTypes), len(bodies))
	}
	seen := make(map[EventType]bool)
	for i, body := range bodies {
		kind := body.EventType()
		if kind != want[i] {
			t.Fatalf("type = %q, want %q", kind, want[i])
		}
		if seen[kind] {
			t.Fatalf("duplicate type %q", kind)
		}
		seen[kind] = true
		data, err := json.Marshal(Event{Body: body})
		if err != nil {
			t.Fatal(err)
		}
		var decoded Event
		if err = json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded.Body, body) {
			t.Fatalf("%s: got %#v, want %#v", kind, decoded.Body, body)
		}
	}
	var event Event
	if err := json.Unmarshal([]byte(`{"type":"unknown.event","body":{}}`), &event); err == nil {
		t.Fatal("unknown event accepted")
	}
}

func TestGeneratedContractCurrent(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "contract")
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	if err := contractgen.Generate(".", output); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"union_gen.go", "../web/src/lib/contract.gen.ts"} {
		expected, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filepath.Join(output, file))
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != string(expected) {
			t.Fatalf("%s is stale; run go generate ./contract", file)
		}
	}
}

func TestPrOpenArgumentsRoundTrip(t *testing.T) {
	input := OpSubmitRequest{Operation: Operation{Room: "room", Kind: "pr.open", Args: PrOpenArgs{Title: "Title", Body: "Body", Base: "main", Draft: true}}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output OpSubmitRequest
	if err := json.Unmarshal(data, &output); err != nil || !reflect.DeepEqual(input, output) {
		t.Fatalf("output %+v error %v", output, err)
	}
}

func TestUnknownOperationKind(t *testing.T) {
	var op Operation
	if err := json.Unmarshal([]byte(`{"kind":"unknown.kind","args":{}}`), &op); err == nil {
		t.Fatal("unknown operation accepted")
	}
}

func TestGeneratedWireTypes(t *testing.T) {
	data, err := os.ReadFile("../web/src/lib/contract.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	ts := string(data)
	if !strings.Contains(ts, "seconds: number;") {
		t.Fatal("floating-point fields must be TypeScript numbers")
	}
	if !strings.Contains(ts, "export type HostName = string;") {
		t.Fatal("registered hosts must be accepted")
	}
	response, err := json.Marshal(EventsListResponse{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(response), `"events":null`) {
		t.Fatalf("unexpected response: %s", response)
	}
	if !strings.Contains(ts, "events: Array<Event> | null;") {
		t.Fatal("events must allow null")
	}
}

func TestBoxMessageRegistry(t *testing.T) {
	bodies := []BoxMessageBody{TurnStart{Turn: "turn_a"}, TurnSteer{Queue: QueueSteer}, TurnStop{Reason: "stop"}, Archive{}, ComputerWatchStart{Generation: 42}, ComputerWatchStop{}, ComputerInputBatch{Events: []ComputerInput{{Kind: InputKeyDown, Key: "a"}}}, ShellWatchStart{Generation: 7}, ShellWatchStop{}}
	want := []BoxMessageType{"turn.start", "turn.steer", "turn.stop", "archive", "computer.watch.start", "computer.watch.stop", "computer.input", "shell.watch.start", "shell.watch.stop"}
	if len(boxMessageTypes) != len(bodies) {
		t.Fatalf("registry has %d entries, want %d", len(boxMessageTypes), len(bodies))
	}
	seen := map[BoxMessageType]bool{}
	for i, body := range bodies {
		kind := body.BoxMessageType()
		if kind != want[i] || seen[kind] {
			t.Fatalf("unexpected box message type %q", kind)
		}
		seen[kind] = true
		data, err := json.Marshal(BoxMessage{Body: body})
		if err != nil {
			t.Fatal(err)
		}
		var got BoxMessage
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Body, body) {
			t.Fatalf("round trip: got %#v, want %#v", got.Body, body)
		}
	}
	var got BoxMessage
	if err := json.Unmarshal([]byte(`{"type":"unknown","body":{}}`), &got); err == nil {
		t.Fatal("unknown box message accepted")
	}
}
