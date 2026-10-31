package runtime

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

// PublishInstructions repairs every reachable host after a local save. A later
// heartbeat retries failures; the committed local save remains authoritative.
func (b *Boxes) PublishInstructions(ctx context.Context) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		log.Printf("instructions: list hosts: %v", err)
		return
	}
	for _, record := range hosts {
		h, err := b.host(ctx, record.Name)
		if err == nil {
			err = b.syncInstructions(ctx, h)
		}
		if err != nil {
			log.Printf("host %s: instructions: %v", record.Name, err)
		}
	}
}

func (b *Boxes) syncInstructions(ctx context.Context, h *podman.Host, extra ...contract.RoomID) error {
	if b.InstructionsDir == "" {
		return nil
	}
	b.instructionsMu.Lock()
	defer b.instructionsMu.Unlock()
	rooms := make(map[contract.RoomID]bool)
	for _, room := range extra {
		rooms[room] = true
	}
	records, err := b.Store.Boxes(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		session, err := b.Store.Session(ctx, record.Session)
		if err != nil {
			return err
		}
		rooms[session.Room] = true
	}
	entries, err := os.ReadDir(filepath.Join(b.InstructionsDir, "rooms"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			rooms[contract.RoomID(entry.Name())] = true
		}
	}
	paths := []string{filepath.Join("global", "AGENTS.md")}
	for room := range rooms {
		if room == "" || filepath.Base(string(room)) != string(room) || room == "." || room == ".." {
			return errors.New("invalid instruction room ID")
		}
		paths = append(paths, filepath.Join("rooms", string(room), "AGENTS.md"))
	}
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(b.InstructionsDir, path))
		if errors.Is(err, os.ErrNotExist) {
			data, err = []byte{}, nil
		}
		if err != nil {
			return err
		}
		if err := h.SyncInstruction(ctx, filepath.Join(b.HostData, "instructions", path), data); err != nil {
			return err
		}
	}
	return nil
}

// instructionHeartbeat retries failed repairs and skips unchanged masters.
func (b *Boxes) instructionHeartbeat(ctx context.Context, name contract.HostName, h *podman.Host) func(bool) {
	var repaired uint64
	return func(ok bool) {
		if !ok {
			return
		}
		generation := h.Generation()
		if generation != 0 && generation == repaired {
			return
		}
		if err := b.syncInstructions(ctx, h); err != nil {
			log.Printf("host %s: instructions: %v", name, err)
			return
		}
		repaired = generation
	}
}
