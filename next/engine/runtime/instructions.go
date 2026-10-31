package runtime

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/resident-labs/dyna/box/podman"
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

func (b *Boxes) syncInstructions(ctx context.Context, h *podman.Host) error {
	if b.InstructionsDir == "" {
		return nil
	}
	b.instructionsMu.Lock()
	if b.instructionSynced == nil {
		b.instructionSynced = make(map[*podman.Host]*instructionHost)
	}
	state := b.instructionSynced[h]
	if state == nil {
		state = &instructionHost{}
		b.instructionSynced[h] = state
	}
	b.instructionsMu.Unlock()
	state.mu.Lock()
	defer state.mu.Unlock()
	version := instructionVersion{h.Generation(), b.instructionSaves.Load()}
	if version.generation != 0 && state.version == version {
		return nil
	}
	paths := []string{filepath.Join("global", "AGENTS.md")}
	entries, err := os.ReadDir(filepath.Join(b.InstructionsDir, "rooms"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			paths = append(paths, filepath.Join("rooms", entry.Name(), "AGENTS.md"))
		}
	}
	var files []podman.InstructionFile
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(b.InstructionsDir, path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		files = append(files, podman.InstructionFile{Path: filepath.Join(b.HostData, "instructions", path), Data: data})
	}
	if err := h.SyncInstructions(ctx, files); err != nil {
		return err
	}
	version.generation = h.Generation()
	state.version = version
	return nil
}
