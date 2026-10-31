package runtime

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"

	"github.com/resident-labs/dyna/box/podman"
)

type instructionVersion struct{ generation, saves uint64 }

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
	defer b.instructionsMu.Unlock()
	version := instructionVersion{h.Generation(), b.instructionSaves.Load()}
	if version.generation != 0 && b.instructionSynced[h] == version {
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
	if b.instructionSynced == nil {
		b.instructionSynced = make(map[*podman.Host]instructionVersion)
	}
	version.generation = h.Generation()
	b.instructionSynced[h] = version
	return nil
}
