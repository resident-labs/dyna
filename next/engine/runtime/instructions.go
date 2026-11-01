package runtime

import (
	"context"
	"errors"
	"log"
	"os"
	"sync"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

type instructionVersion struct{ generation, saves uint64 }

type instructionHost struct {
	mu      sync.Mutex
	version instructionVersion
}

// PublishInstructions marks the local commit before starting remote work.
func (b *Boxes) PublishInstructions(ctx context.Context) {
	b.instructionSaves.Add(1)
	go func() {
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
	}()
}

// settle waits out a copy already running, which could still write into a deleted room's folder. Any later copy skips that room.
func (s *instructionHost) settle() {
	s.mu.Lock()
	defer s.mu.Unlock()
}

func (b *Boxes) instructionState(h *podman.Host) *instructionHost {
	b.instructionsMu.Lock()
	defer b.instructionsMu.Unlock()
	if b.instructionSynced == nil {
		b.instructionSynced = make(map[*podman.Host]*instructionHost)
	}
	state := b.instructionSynced[h]
	if state == nil {
		state = &instructionHost{}
		b.instructionSynced[h] = state
	}
	return state
}

func (b *Boxes) syncInstructions(ctx context.Context, h *podman.Host) error {
	if b.Layout.Data == "" {
		return nil
	}
	state := b.instructionState(h)
	state.mu.Lock()
	defer state.mu.Unlock()
	version := instructionVersion{h.Generation(), b.instructionSaves.Load()}
	if version.generation != 0 && state.version == version {
		return nil
	}
	// A room being deleted is left out, or this copy would bring back the folder the deletion removes.
	rooms, err := b.Store.OpenRooms(ctx)
	if err != nil {
		return err
	}
	var files []podman.InstructionFile
	for _, room := range append([]contract.RoomID{""}, rooms...) {
		data, err := os.ReadFile(b.Layout.Instructions(room))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		files = append(files, podman.InstructionFile{Path: b.Layout.HostInstructions(room), Data: data})
	}
	if err := h.SyncInstructions(ctx, files); err != nil {
		return err
	}
	version.generation = h.Generation()
	state.version = version
	return nil
}
