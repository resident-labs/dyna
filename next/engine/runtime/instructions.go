package runtime

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
)

type instructionVersion struct{ generation, saves uint64 }

type instructionHost struct {
	gate    chan struct{}
	version instructionVersion
}

// PublishInstructions marks the local commit before starting remote work.
func (b *Boxes) PublishInstructions(ctx context.Context) {
	b.instructionSaves.Add(1)
	go b.publishInstructions(ctx)
}

// publishInstructions copies the files to every host. An API save and an edit the watch finds both end here. It notes
// the files first, so the watch does not publish an API save a second time.
func (b *Boxes) publishInstructions(ctx context.Context) {
	if _, err := b.instructionsChanged(ctx); err != nil {
		log.Printf("instructions: %v", err)
	}
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

// settle waits out a copy already running, which could still write into a deleted room's folder. Any later copy skips that room.
func (s *instructionHost) settle(ctx context.Context) error {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-s.gate
	return nil
}

func (b *Boxes) instructionState(h *podman.Host) *instructionHost {
	b.instructionsMu.Lock()
	defer b.instructionsMu.Unlock()
	if b.instructionSynced == nil {
		b.instructionSynced = make(map[*podman.Host]*instructionHost)
	}
	state := b.instructionSynced[h]
	if state == nil {
		state = &instructionHost{gate: make(chan struct{}, 1)}
		b.instructionSynced[h] = state
	}
	return state
}

func (b *Boxes) syncInstructions(ctx context.Context, h *podman.Host) error {
	if b.Layout.Data == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, box.StartPreparationBudget)
	defer cancel()
	state := b.instructionState(h)
	select {
	case state.gate <- struct{}{}:
		defer func() { <-state.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
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
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			files = append(files, podman.InstructionFile{Path: b.Layout.HostInstructions(room), Data: data})
		}
		if b.Addons != nil {
			for _, rootOnly := range []bool{false, true} {
				bundle, err := b.Addons.Bundle(room, rootOnly)
				if err != nil {
					return err
				}
				path := b.Layout.HostAddons(room)
				if rootOnly {
					path = b.Layout.HostRootAddons(room)
				}
				if bundle != nil {
					files = append(files, podman.InstructionFile{Path: path, Data: bundle})
				}
			}
		}
	}
	if err := h.SyncInstructions(ctx, files); err != nil {
		return err
	}
	version.generation = h.Generation()
	state.version = version
	return nil
}
