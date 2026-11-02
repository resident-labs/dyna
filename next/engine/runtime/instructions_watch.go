package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"log"
	"os"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// watchInstructionsEvery is how often the engine looks for an AGENTS.md edited on disk.
const watchInstructionsEvery = 3 * time.Second

// watchInstructions publishes the global AGENTS.md and every open room's whenever one changes on disk, however it was
// edited: by an API save, or in place, by an editor that saves by rename, or by a root's bash on the engine host. It
// hashes the files rather than watching them, so a replaced file or a new room's first file needs no new watch.
func (b *Boxes) watchInstructions(ctx context.Context) {
	for {
		if err := b.checkInstructions(ctx); err != nil && ctx.Err() == nil {
			log.Printf("watch instructions: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchInstructionsEvery):
		}
	}
}

// checkInstructions publishes when a file changed since it was last noted.
func (b *Boxes) checkInstructions(ctx context.Context) error {
	changed, err := b.instructionsChanged(ctx)
	if err != nil || !changed {
		return err
	}
	b.instructionSaves.Add(1)
	b.publishInstructions(ctx)
	return nil
}

// instructionsChanged hashes the global file and each open room's and notes the sums. It reports whether any file is
// new or differs from the last note. A removed file is not a change, since a host keeps the copy it has.
func (b *Boxes) instructionsChanged(ctx context.Context) (bool, error) {
	if b.Layout.Data == "" {
		return false, nil
	}
	rooms, err := b.Store.OpenRooms(ctx)
	if err != nil {
		return false, err
	}
	sums := make(map[contract.RoomID][sha256.Size]byte, len(rooms)+1)
	for _, room := range append([]contract.RoomID{""}, rooms...) {
		data, err := os.ReadFile(b.Layout.Instructions(room))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		sums[room] = sha256.Sum256(data)
	}
	b.instructionsMu.Lock()
	defer b.instructionsMu.Unlock()
	changed := false
	for room, sum := range sums {
		if noted, ok := b.instructionSums[room]; !ok || noted != sum {
			changed = true
		}
	}
	b.instructionSums = sums
	return changed, nil
}
