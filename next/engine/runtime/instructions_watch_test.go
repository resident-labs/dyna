package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

func watchedBoxes(t *testing.T) (*Boxes, *instructionTransport) {
	t.Helper()
	transport := &instructionTransport{}
	host, err := podman.New("user@host", t.TempDir(), "host", transport)
	if err != nil {
		t.Fatal(err)
	}
	layout := box.Layout{Data: t.TempDir(), HostData: t.TempDir()}
	return &Boxes{Store: instructionStore{}, Layout: layout, hosts: map[contract.HostName]*podman.Host{"host": host}}, transport
}

// saveByRename replaces a file the way most editors do: a new file renamed over the old one.
func saveByRename(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	next := path + ".swp"
	if err := os.WriteFile(next, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
}

func mirrored(t *testing.T, b *Boxes, room contract.RoomID) string {
	t.Helper()
	got, err := os.ReadFile(b.Layout.HostInstructions(room))
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// An AGENTS.md edited in place on the engine host, with no API call, reaches the host's copy.
func TestAFileEditReachesTheHostWithoutAnAPICall(t *testing.T) {
	ctx := context.Background()
	b, _ := watchedBoxes(t)
	if err := b.checkInstructions(ctx); err != nil {
		t.Fatal(err)
	}
	saveByRename(t, b.Layout.Instructions("room"), "room, first")
	if err := os.WriteFile(b.Layout.Instructions(""), []byte("global, first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.checkInstructions(ctx); err != nil {
		t.Fatal(err)
	}
	if got := mirrored(t, b, "room"); got != "room, first" {
		t.Fatalf("room copy %q", got)
	}
	if got := mirrored(t, b, ""); got != "global, first" {
		t.Fatalf("global copy %q", got)
	}
	saveByRename(t, b.Layout.Instructions("room"), "room, second")
	if err := b.checkInstructions(ctx); err != nil {
		t.Fatal(err)
	}
	if got := mirrored(t, b, "room"); got != "room, second" {
		t.Fatalf("room copy after a rename save %q", got)
	}
}

// A file nobody changed publishes nothing, and neither does one a publish already carried.
func TestAnUnchangedFilePublishesNothing(t *testing.T) {
	ctx := context.Background()
	b, transport := watchedBoxes(t)
	saveByRename(t, b.Layout.Instructions(""), "global")
	if err := b.checkInstructions(ctx); err != nil {
		t.Fatal(err)
	}
	saves, commands := b.instructionSaves.Load(), transport.commands
	if saves == 0 || commands == 0 {
		t.Fatal("the first look published nothing")
	}
	if err := b.checkInstructions(ctx); err != nil {
		t.Fatal(err)
	}
	if b.instructionSaves.Load() != saves || transport.commands != commands {
		t.Fatal("an unchanged file was published")
	}
	// An API save publishes on its own; the next look finds nothing new.
	saveByRename(t, b.Layout.Instructions(""), "saved over the API")
	b.instructionSaves.Add(1)
	b.publishInstructions(ctx)
	saves, commands = b.instructionSaves.Load(), transport.commands
	if err := b.checkInstructions(ctx); err != nil {
		t.Fatal(err)
	}
	if b.instructionSaves.Load() != saves || transport.commands != commands {
		t.Fatal("the look published an API save again")
	}
	if got := mirrored(t, b, ""); got != "saved over the API" {
		t.Fatalf("global copy %q", got)
	}
}
