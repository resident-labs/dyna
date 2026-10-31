package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type instructionStore struct{ boxStore }

func (instructionStore) Box(_ context.Context, id contract.BoxID) (box.Box, error) {
	return box.Box{ID: id, Host: "host", Session: "session"}, nil
}
func (instructionStore) Hosts(context.Context) ([]box.Host, error) {
	return []box.Host{{Name: "host"}}, nil
}
func (instructionStore) Boxes(context.Context) ([]box.Box, error) {
	return []box.Box{{Session: "session"}}, nil
}
func (instructionStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Room: "room"}, nil
}

type instructionTransport struct{ offline bool }

func (r *instructionTransport) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	if r.offline {
		return nil, errors.New("offline")
	}
	if strings.Contains(args[len(args)-1], "'podman'") {
		return []byte("DYNA-END 00000000000000000000 000\n"), nil
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", args[len(args)-1])
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	return out, nil
}
func TestInstructionsRepairAfterOfflineSave(t *testing.T) {
	ctx := context.Background()
	data, hostData := t.TempDir(), t.TempDir()
	source := filepath.Join(data, "rooms", "room", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &instructionTransport{offline: true}
	host, err := podman.New("user@host", t.TempDir(), "host", transport)
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: instructionStore{}, InstructionsDir: data, HostData: hostData, hosts: map[contract.HostName]*podman.Host{"host": host}}
	b.PublishInstructions(ctx)
	heartbeat := b.instructionHeartbeat(ctx, "host", host)
	heartbeat(true) // Failed repair stays pending.
	transport.offline = false
	heartbeat(true) // The next healthy heartbeat retries.
	got, err := os.ReadFile(filepath.Join(hostData, "instructions", "rooms", "room", "AGENTS.md"))
	if err != nil || string(got) != "new" {
		t.Fatalf("got %q: %v", got, err)
	}
}

func TestInstructionMountsAreRoomScoped(t *testing.T) {
	b := &Boxes{HostData: "/host"}
	_, spec := b.container(lifecycle.BoxSpec{Room: "room"}, "image")
	found := map[string]box.Mount{}
	for _, mount := range spec.Mounts {
		found[mount.BoxPath] = mount
	}
	for path, source := range map[string]string{
		"/run/dyna/instructions/global": "/host/instructions/global",
		"/run/dyna/instructions/room":   "/host/instructions/rooms/room",
	} {
		mount := found[path]
		if mount.HostPath != source || !mount.ReadOnly {
			t.Fatalf("mount %s: %+v", path, mount)
		}
	}
}

func TestResumeRepairsInstructionsBeforeStarting(t *testing.T) {
	ctx := context.Background()
	data, remote := t.TempDir(), t.TempDir()
	path := filepath.Join(data, "rooms", "room", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &instructionTransport{offline: true}
	host, err := podman.New("user@host", t.TempDir(), "host", transport)
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: instructionStore{}, InstructionsDir: data, HostData: remote, hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}}
	if err := b.Start(ctx, "box"); err == nil {
		t.Fatal("started with offline host")
	}
	transport.offline = false
	if err := b.Start(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(remote, "instructions", "rooms", "room", "AGENTS.md"))
	if err != nil || string(got) != "saved" {
		t.Fatalf("got %q: %v", got, err)
	}
}
