package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract/internal/contractgen"
)

func TestEventRegistry(t *testing.T) {
	bodies := []EventBody{PostCreated{Text: "hello"}, SessionStarted{Name: "agent"}, SessionClosed{Session: "ses_a"}, TurnStarted{Turn: "turn_a"}, TurnEnded{Final: "done"}, BoxChanged{To: BoxReady}}
	want := []EventType{"post.created", "session.started", "session.closed", "turn.started", "turn.ended", "box.changed"}
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

func TestUnknownOperationKind(t *testing.T) {
	var op Operation
	if err := json.Unmarshal([]byte(`{"kind":"unknown.kind","args":{}}`), &op); err == nil {
		t.Fatal("unknown operation accepted")
	}
}
