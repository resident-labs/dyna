package room

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageWakeUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		wake        bool
	}{
		{"legacy ordinary", `{"kind":"message"}`, true},
		{"legacy instructions", `{"kind":"instructions"}`, false},
		{"explicit quiet", `{"kind":"message","wake":false}`, false},
		{"explicit wake", `{"kind":"message","wake":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var msg Message
			if err := json.Unmarshal([]byte(tc.input), &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Wake != tc.wake {
				t.Fatalf("wake = %v, want %v", msg.Wake, tc.wake)
			}
		})
	}
	data, err := json.Marshal(Message{Kind: "instructions"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"wake":false`) {
		t.Fatalf("quiet post omitted explicit intent: %s", data)
	}
}
