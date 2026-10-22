package callspec

import (
	"encoding/json"
	"testing"
)

func TestExecutionRefValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  ExecutionRef
		want bool
	}{
		{"session before admission", ExecutionRef{Session: "s", Turn: 1}, true},
		{"workflow before admission", ExecutionRef{Run: "r", Agent: 1}, true},
		{"admitted session", ExecutionRef{Session: "s", Turn: 2, AdmissionID: "s/7"}, true},
		{"admitted workflow", ExecutionRef{Run: "r", Agent: 2, AdmissionID: "r/7"}, true},
		{"empty", ExecutionRef{}, false},
		{"session without turn", ExecutionRef{Session: "s"}, false},
		{"turn without session", ExecutionRef{Turn: 1}, false},
		{"negative turn", ExecutionRef{Session: "s", Turn: -1}, false},
		{"run without agent", ExecutionRef{Run: "r"}, false},
		{"agent without run", ExecutionRef{Agent: 1}, false},
		{"negative agent", ExecutionRef{Run: "r", Agent: -1}, false},
		{"both parents", ExecutionRef{Session: "s", Turn: 1, Run: "r", Agent: 1}, false},
		{"session with run", ExecutionRef{Session: "s", Turn: 1, Run: "r"}, false},
		{"session with agent", ExecutionRef{Session: "s", Turn: 1, Agent: 1}, false},
		{"run with session", ExecutionRef{Run: "r", Agent: 1, Session: "s"}, false},
		{"run with turn", ExecutionRef{Run: "r", Agent: 1, Turn: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.ref.Validate(); (err == nil) != tc.want {
				t.Fatalf("Validate(%+v) = %v; valid = %v", tc.ref, err, tc.want)
			}
		})
	}
}

func TestExecutionRefJSON(t *testing.T) {
	ref := ExecutionRef{Session: "s", Turn: 2, AdmissionID: "s/7"}
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"session":"s","turn":2,"admissionId":"s/7"}` {
		t.Fatalf("JSON = %s", raw)
	}
	var got ExecutionRef
	if err := json.Unmarshal(raw, &got); err != nil || got != ref {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, ref)
	}
}
