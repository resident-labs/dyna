package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
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
func (instructionStore) OpenRooms(context.Context) ([]contract.RoomID, error) {
	return []contract.RoomID{"room"}, nil
}
func (instructionStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Room: "room"}, nil
}

type instructionTransport struct {
	offline  bool
	commands int
}

func (r *instructionTransport) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	r.commands++
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
	if err := os.WriteFile(source, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &instructionTransport{}
	host, err := podman.New("user@host", t.TempDir(), "host", transport)
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: instructionStore{}, Layout: box.Layout{Data: data, HostData: hostData}, hosts: map[contract.HostName]*podman.Host{"host": host}}
	heartbeat := func(ok bool) {
		if ok {
			_ = b.syncInstructions(ctx, host)
		}
	}
	heartbeat(true)
	count := transport.commands
	heartbeat(true) // Unchanged host needs no command.
	if transport.commands != count {
		t.Fatal("unchanged host was synced")
	}
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	transport.offline = true
	b.instructionSaves.Add(1)
	heartbeat(true) // Failed repair stays pending.
	transport.offline = false
	heartbeat(true) // The next healthy heartbeat retries.
	got, err := os.ReadFile(filepath.Join(hostData, "rooms", "room", "instructions", "AGENTS.md"))
	if err != nil || string(got) != "new" {
		t.Fatalf("got %q: %v", got, err)
	}
}

type noRoomsStore struct{ instructionStore }

func (noRoomsStore) OpenRooms(context.Context) ([]contract.RoomID, error) { return nil, nil }

// A heartbeat copy must not bring back a folder that a room's deletion removes.
func TestInstructionsSkipARoomBeingDeleted(t *testing.T) {
	ctx := context.Background()
	layout := box.Layout{Data: t.TempDir(), HostData: t.TempDir()}
	source := layout.Instructions("room")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("room"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.Instructions(""), []byte("global"), 0600); err != nil {
		t.Fatal(err)
	}
	host, err := podman.New("user@host", t.TempDir(), "host", &instructionTransport{})
	if err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: noRoomsStore{}, Layout: layout, hosts: map[contract.HostName]*podman.Host{"host": host}}
	if err := b.syncInstructions(ctx, host); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(layout.HostInstructions("room"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("room copy: %v", err)
	}
	if got, err := os.ReadFile(layout.HostInstructions("")); err != nil || string(got) != "global" {
		t.Fatalf("global copy %q: %v", got, err)
	}
}

func TestRoomRemovalRefusesAnEmptyRoom(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	if err := b.RemoveRoom(context.Background(), "host", ""); err == nil {
		t.Fatal("an empty room would name every room")
	}
}

func TestInstructionMountsAreRoomScoped(t *testing.T) {
	b := &Boxes{Layout: box.Layout{HostData: "/host"}}
	_, spec := b.container(lifecycle.BoxSpec{Room: "room"}, "image", box.Mount{})
	found := map[string]box.Mount{}
	for _, mount := range spec.Mounts {
		found[mount.BoxPath] = mount
	}
	for path, source := range map[string]string{
		"/run/dyna/instructions/global": "/host/instructions/global",
		"/run/dyna/instructions/room":   "/host/rooms/room/instructions",
	} {
		mount := found[path]
		if mount.HostPath != source || !mount.ReadOnly || !mount.MakeDir {
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
	b := &Boxes{Store: instructionStore{}, Layout: box.Layout{Data: data, HostData: remote}, hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}, installed: map[contract.HostName]bool{"host": true}}
	if err := b.Start(ctx, "box"); err == nil {
		t.Fatal("started with offline host")
	}
	transport.offline = false
	if err := b.Start(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(remote, "rooms", "room", "instructions", "AGENTS.md"))
	if err != nil || string(got) != "saved" {
		t.Fatalf("got %q: %v", got, err)
	}
}

type addHostStore struct{ instructionStore }

func (addHostStore) AddHost(_ context.Context, _ box.Host) error { return nil }

type admissionTransport struct{}

func (admissionTransport) Run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	if strings.Contains(command, "dyna-instructions") {
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Stdin = bytes.NewReader(input)
		return cmd.Output()
	}
	var body string
	switch {
	case strings.Contains(command, "'uname' '-m'"):
		if goruntime.GOARCH == "arm64" {
			body = "aarch64"
		} else {
			body = "x86_64"
		}
	case strings.Contains(command, "_NPROCESSORS_ONLN"):
		body = "8 17179869184 17179869184 107374182400"
	case strings.Contains(command, "dyna-probe"):
		body = "test-sum"
	case strings.Contains(command, "SwapTotal:"):
		body = "0"
	case strings.Contains(command, "'curl'"):
		match := regexp.MustCompile(`probe-([0-9a-f]{32})`).FindStringSubmatch(command)
		if len(match) != 2 {
			return nil, errors.New("missing forward token")
		}
		body = match[1]
	case strings.Contains(command, "'/etc/machine-id'"):
		body = "0123456789abcdef0123456789abcdef"
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d 000\n", body, len(body))), nil
}

func TestAddHostCopiesPreviouslySavedInstructions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data, remote := t.TempDir(), t.TempDir()
	source := filepath.Join(data, "rooms", "room", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("saved before host"), 0600); err != nil {
		t.Fatal(err)
	}
	machineID := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(machineID, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &Boxes{Store: addHostStore{}, Runtime: t.TempDir(), Layout: box.Layout{Data: data, HostData: remote}, Version: "dev", Context: ctx, hostRunner: admissionTransport{}, machineIDFile: machineID}
	b.binOnce.Do(func() { b.binSum = "test-sum" })
	if _, err := b.AddHost(ctx, contract.HostAddRequest{Name: "host", SSH: "user@host", OwnerOnlyAdmin: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(remote, "rooms", "room", "instructions", "AGENTS.md"))
	if err != nil || string(got) != "saved before host" {
		t.Fatalf("got %q: %v", got, err)
	}
}

// delayedInstructionStore holds the background publish, which asks first, before it can reach a host.
type delayedInstructionStore struct {
	instructionStore
	entered, release chan struct{}
	once             *sync.Once
}

func (s delayedInstructionStore) Hosts(context.Context) ([]box.Host, error) {
	first := false
	s.once.Do(func() { first = true })
	if !first {
		return engineHosts("host"), nil
	}
	close(s.entered)
	<-s.release
	return nil, nil
}

func TestSaveThenResumeRepairsBeforeDelayedPublish(t *testing.T) {
	ctx := context.Background()
	data, remote := t.TempDir(), t.TempDir()
	path := filepath.Join(data, "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &instructionTransport{}
	host, err := podman.New("user@host", t.TempDir(), "host", transport)
	if err != nil {
		t.Fatal(err)
	}
	delayed := delayedInstructionStore{entered: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	defer close(delayed.release)
	b := &Boxes{Store: delayed, Layout: box.Layout{Data: data, HostData: remote}, hosts: map[contract.HostName]*podman.Host{"host": host}, restored: map[contract.HostName]bool{"host": true}, installed: map[contract.HostName]bool{"host": true}}
	if err := b.syncInstructions(ctx, host); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := engine.New(st, nil, nil, nil)
	service.SetLayout(box.Layout{Data: data})
	service.SetInstructionsPublisher(b.PublishInstructions)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	before, problem := service.InstructionsGet(ctx, owner, contract.InstructionsGetRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	_, problem = service.InstructionsSet(ctx, owner, contract.InstructionsSetRequest{Base: before.Hash, Text: "new"})
	if problem != nil {
		t.Fatal(problem)
	}
	if b.instructionSaves.Load() != 1 {
		t.Fatal("save was not marked before return")
	}
	<-delayed.entered
	if err := b.Start(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(remote, "instructions", "global", "AGENTS.md"))
	if err != nil || string(got) != "new" {
		t.Fatalf("got %q: %v", got, err)
	}
}
