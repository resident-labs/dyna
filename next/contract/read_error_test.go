package contract

import (
	"strings"
	"testing"
)

func TestReadErrorReadsJSONOrPlainText(t *testing.T) {
	for body, want := range map[string]Error{
		`{"code":"invalid","message":"unknown session @ses_1"}`: {Code: ErrInvalid, Message: "unknown session @ses_1"},
		"too large\n": {Message: "too large"},
		"":            {},
	} {
		if got := ReadError(strings.NewReader(body)); got != want {
			t.Errorf("%q: %+v", body, got)
		}
	}
	if got := ReadError(strings.NewReader(strings.Repeat("x", 2*errorBytes))); len(got.Message) != errorBytes {
		t.Errorf("read %d bytes of a long refusal", len(got.Message))
	}
}
