package podman

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type maintenanceRunner struct{ command string }

func (r *maintenanceRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, a := range args {
		if a == "-O" || a == "-M" {
			return nil, nil
		}
	}
	r.command = args[len(args)-1]
	return []byte(fmt.Sprintf("DYNA-END %020d %03d\n", 0, 0)), nil
}
func TestRemoveRoomRunsInTheUserNamespace(t *testing.T) {
	r := &maintenanceRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveRoom(context.Background(), "/data/rooms/room_1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.command, "'podman' 'unshare' 'rm' '-rf' '--' '/data/rooms/room_1'") {
		t.Fatal(r.command)
	}
}

func TestMaintenanceCommands(t *testing.T) {
	r := &maintenanceRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Maintain(context.Background(), "/data"); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"podman image prune -f", "git -C", "gc --no-prune", "/data", "/rooms/*/shared"} {
		if !strings.Contains(r.command, expected) {
			t.Fatalf("missing %q in %s", expected, r.command)
		}
	}
	if strings.Contains(r.command, "prune -a") {
		t.Fatalf("unsafe prune: %s", r.command)
	}
}
