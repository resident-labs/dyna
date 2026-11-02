package podman

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

// The frame puts a failed command's stderr after its stdout, and the error ends with its last lines, so a box's reason and
// the engine's log say what Podman said instead of only its status.
func TestFailedCommandErrorCarriesItsLastLines(t *testing.T) {
	statfs := "Error: statfs /home/pip/.local/share/dyna-next-host/share/rooms/room_1/shared: no such file or directory"
	h := newTestHost(t, &runner{body: "abc123\n" + statfs + "\n", status: 125})
	err := h.CreateContainer(context.Background(), box.ContainerSpec{Box: "box_1", Image: "image", Slice: "dyna-box_1.slice"})
	if err == nil || err.Error() != "host command exited with status 125: abc123; "+statfs {
		t.Fatalf("err = %v", err)
	}
	var exit remoteExit
	if !errors.As(err, &exit) || exit != 125 {
		t.Fatalf("status lost: %v", err)
	}
}

func TestFailedCommandWithNoOutputSaysOnlyItsStatus(t *testing.T) {
	err := newTestHost(t, &runner{status: 125}).StartContainer(context.Background(), "box_1")
	if err == nil || err.Error() != "host command exited with status 125" {
		t.Fatalf("err = %v", err)
	}
}

// Callers that compare statuses still see them through the output.
func TestFailedCommandKeepsItsStatusForCallers(t *testing.T) {
	h := newTestHost(t, &runner{body: "Error: no such volume\n", status: 1})
	if err := h.RemoveVolume(context.Background(), "box_1"); err != nil {
		t.Fatalf("a missing volume is not an error: %v", err)
	}
	h = newTestHost(t, &runner{body: "mountpoint: not a mount point\n", status: 253})
	err := h.makeMountFolder(context.Background(), box.Mount{HostPath: "/host/share/rooms/r/shared", OnMount: "/host/share/rooms"})
	var down box.ShareDown
	if !errors.As(err, &down) {
		t.Fatalf("err = %v", err)
	}
}

func TestFailureTailIsMaskedAndBounded(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 36)
	cases := map[string]struct {
		out  string
		want func(string) bool
	}{
		"masked": {out: "pulling\nError: auth failed for GH_TOKEN=" + secret + "\n", want: func(s string) bool {
			return !strings.Contains(s, secret) && strings.HasSuffix(s, "GH_TOKEN=[masked]")
		}},
		"last lines": {out: "1\n2\n\n3\n4\n5\n6\n7\n", want: func(s string) bool { return s == "3; 4; 5; 6; 7" }},
		"long line": {out: strings.Repeat("x", 4000) + "end", want: func(s string) bool {
			return len(s) == failureBytes+3 && strings.HasPrefix(s, "...") && strings.HasSuffix(s, "end")
		}},
		"huge output": {out: strings.Repeat("line\n", 100000) + "Error: the end\n", want: func(s string) bool {
			return s == "line; line; line; line; Error: the end"
		}},
		"invalid utf-8": {out: "Error: \xff\xfebad\n", want: func(s string) bool { return s == "Error: bad" }},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := outputTail([]byte(c.out)); !c.want(got) {
				t.Fatalf("tail = %q", got)
			}
		})
	}
}

// HoldsBox says whether a closing box has anything on its host to archive.
func TestHoldsBox(t *testing.T) {
	for name, c := range map[string]struct {
		container, volume string
		holds             bool
		fails             bool
	}{
		"container":   {container: "exit 0", volume: "exit 1", holds: true},
		"volume only": {container: "exit 1", volume: "exit 0", holds: true},
		"neither":     {container: "exit 1", volume: "exit 1"},
		"podman down": {container: "echo 'Error: cannot connect' >&2; exit 125", volume: "exit 1", fails: true},
	} {
		t.Run(name, func(t *testing.T) {
			tools := fakeTools(t, map[string]string{"podman": `case "$1 $2 $3" in
"container exists box_1") ` + c.container + ` ;;
"volume exists dyna-box-box_1") ` + c.volume + ` ;;
*) exit 2 ;;
esac`})
			h, err := New("user@host", t.TempDir(), "host", localShell{path: tools + ":" + os.Getenv("PATH")})
			if err != nil {
				t.Fatal(err)
			}
			holds, err := h.HoldsBox(context.Background(), "box_1")
			if c.fails {
				if err == nil || !strings.HasSuffix(err.Error(), "status 125: Error: cannot connect") {
					t.Fatalf("holds %v, err %v", holds, err)
				}
				return
			}
			if err != nil || holds != c.holds {
				t.Fatalf("holds %v, err %v", holds, err)
			}
		})
	}
}
