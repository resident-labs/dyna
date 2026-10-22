package shellquote

import (
	"os/exec"
	"testing"
)

// The shell must read each quoted value back as the same single word.
func TestQuoteRoundTripsThroughShell(t *testing.T) {
	for _, value := range []string{"plain", "with space", "it's", "'", "$HOME `id` \"x\"", ""} {
		out, err := exec.Command("sh", "-c", "printf %s "+Quote(value)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != value {
			t.Errorf("Quote(%q) read back as %q", value, out)
		}
	}
}
