//go:build linux

package shell

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

var schedIdle = regexp.MustCompile(`(?m)^policy\s+:\s+5$`)

func TestCommandsRunUnderSchedIdle(t *testing.T) {
	var out strings.Builder
	p, err := startBash("cat /proc/self/sched; bash -c 'cat /proc/self/sched'", "", "call", &out)
	if err != nil {
		t.Fatal(err)
	}
	p.Wait()
	// Policy 5 is SCHED_IDLE. The command and its child both have it.
	if got := schedIdle.FindAllString(out.String(), -1); len(got) != 2 {
		t.Fatalf("sched output %q: want policy 5 twice", out.String())
	}
	plain, err := exec.Command("cat", "/proc/self/sched").Output()
	if err != nil {
		t.Fatal(err)
	}
	if schedIdle.Match(plain) {
		t.Fatal("a command started without StartIdle has the idle policy: the test cannot tell")
	}
}
