package native

import (
	"encoding/json"
	"testing"
)

func TestPiDefaultLabel(t *testing.T) {
	token := testJWT(map[string]string{"email": "person@example.test"})
	for _, tc := range []struct {
		entry piAuthEntry
		want  string
	}{
		{piAuthEntry{Type: "oauth", Access: token}, "person@example.test"},
		{piAuthEntry{Type: "oauth", Access: "opaque"}, "provider login"},
		{piAuthEntry{Type: "api_key", Key: "secret1234"}, "provider · 1234"},
		{piAuthEntry{Type: "api_key", Key: "1234"}, "provider key"},
		{piAuthEntry{Type: "api_key", Key: "secret\n123"}, "provider key"},
	} {
		raw, err := json.Marshal(tc.entry)
		if err != nil {
			t.Fatal(err)
		}
		if got := PiDefaultLabel("provider", raw); got != tc.want {
			t.Errorf("label = %q, want %q", got, tc.want)
		}
	}
}
