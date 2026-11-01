package podman

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
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
	if _, err := h.Maintain(context.Background(), box.Layout{HostData: "/data"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"podman image prune -f", "git -C", "gc --no-prune", "'/data/git' '/data/rooms' 'shared'"} {
		if !strings.Contains(r.command, expected) {
			t.Fatalf("missing %q in %s", expected, r.command)
		}
	}
	if strings.Contains(r.command, "prune -a") {
		t.Fatalf("unsafe prune: %s", r.command)
	}
}

// localShell runs host commands here, so the maintenance script meets real git and real files.
type localShell struct{ path string }

func (r localShell) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") || slices.Contains(args, "-M") {
		return nil, nil
	}
	c := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	c.Env = append(os.Environ(), "PATH="+r.path)
	c.Stdin = bytes.NewReader(input)
	return c.Output()
}

// git gc prints "Nothing new to pack." to stdout on an empty cache, which must not become part of a room name.
func TestMaintainOutputHoldsOnlyRoomSizes(t *testing.T) {
	data := t.TempDir()
	layout := box.Layout{HostData: data}
	cache, err := layout.HostGit("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--bare", cache).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := os.MkdirAll(layout.HostShared("room_1"), 0700); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h, err := New("user@host", t.TempDir(), "host", localShell{path: bin + ":" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	sizes, err := h.Maintain(context.Background(), layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 1 || sizes[0].Room != "room_1" {
		t.Fatalf("sizes %+v", sizes)
	}
}
