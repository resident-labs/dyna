//go:build linux

package shell

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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
	// A test run by an agent's shell command is itself at 19, so a plain exec is compared with the test's own value.
	own, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Getpriority returns 20 - nice on Linux.
	ownNice := 20 - own
	if ownNice == 19 {
		t.Log("this test already runs at nice 19, so it cannot tell a command started with StartLowPriority from a plain one")
		return
	}
	plain, err := exec.Command(niceOf).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(plain)); got != strconv.Itoa(ownNice) {
		t.Fatalf("a command started without StartLowPriority has nice %s, want the test's own %d", got, ownNice)
	}
}
