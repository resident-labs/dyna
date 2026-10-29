package room

import (
	"strings"
	"testing"
)

func TestParseAttribution(t *testing.T) {
	for value, want := range map[string]string{"": "", " OFF ": "", "  Opened by an agent  ": "Opened by an agent"} {
		if got, err := ParseAttribution(value); err != nil || got != want {
			t.Fatalf("ParseAttribution(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"two\nlines", strings.Repeat("x", MaxAttributionLen+1)} {
		if _, err := ParseAttribution(value); err == nil {
			t.Fatalf("ParseAttribution(%.20q) was accepted", value)
		}
	}
}
