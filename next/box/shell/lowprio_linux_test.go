//go:build linux

package shell

import (
	"os/exec"
	"strings"
	"testing"
)

// niceOf prints the nice value of the process that runs it.
const niceOf = "nice"

func TestCommandsRunAtLowPriority(t *testing.T) {
	var out strings.Builder
	p, err := startBash(niceOf+"; bash -c "+niceOf, "", "call", &out)
	if err != nil {
		t.Fatal(err)
	}
	p.Wait()
	if got := strings.Fields(out.String()); len(got) != 2 || got[0] != "19" || got[1] != "19" {
		t.Fatalf("nice values %q: want the command and its child at 19", out.String())
	}
	plain, err := exec.Command(niceOf).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(plain)); got != "0" {
		t.Fatalf("a command started without StartLowPriority has nice %s, want 0", got)
	}
}
