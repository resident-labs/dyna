package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostexecGrantReason(t *testing.T) {
	for _, tc := range []struct {
		reason string
		valid  bool
	}{
		{"Inspect host logs", true}, {"", false}, {"two\nlines", false}, {strings.Repeat("x", 301), false},
	} {
		data, err := json.Marshal(struct {
			Room   RoomID     `json:"room"`
			To     Principal  `json:"to"`
			Hosts  []HostName `json:"hosts"`
			Reason string     `json:"reason"`
		}{"room", Principal{Kind: PrincipalSession, Session: "s"}, []HostName{"host"}, tc.reason})
		if err != nil {
			t.Fatal(err)
		}
		var args HostexecGrantArgs
		err = json.Unmarshal(data, &args)
		if (err == nil) != tc.valid {
			t.Fatalf("reason %q: %v", tc.reason, err)
		}
	}
}
