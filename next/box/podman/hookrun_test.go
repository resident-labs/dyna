package podman

import (
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func TestARunFiredByAnEventReadsItOnStdin(t *testing.T) {
	args := runArgs(box.RunContainer{ID: "run_a", Image: "base", Command: []string{"bash", "watch.sh"}, Event: "github.pr.opened"})
	line := strings.Join(args, " ")
	if !strings.Contains(line, "--env DYNA_EVENT=github.pr.opened") {
		t.Fatal(line)
	}
	at := slices.Index(args, "--")
	if want := []string{"/bin/sh", "-c", `exec "$@" < /box/hook/` + box.RunEventFile, "dyna-run", "bash", "watch.sh"}; !slices.Equal(args[at+1:], want) {
		t.Fatalf("%q", args[at+1:])
	}
}

func TestAScheduledRunHasNoEvent(t *testing.T) {
	args := runArgs(box.RunContainer{ID: "run_a", Image: "base", Command: []string{"bash", "watch.sh"}})
	if strings.Contains(strings.Join(args, " "), "DYNA_EVENT") {
		t.Fatal(args)
	}
	if at := slices.Index(args, "--"); !slices.Equal(args[at+1:], []string{"bash", "watch.sh"}) {
		t.Fatalf("%q", args[at+1:])
	}
}
