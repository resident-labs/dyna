package runtime

import (
	"context"
	"log"

	"github.com/resident-labs/dyna/box/podman"
)

// repairBinary converges a good survey's link without holding up the next probe or duplicating an in-flight repair.
// The normal shared installer owns byte-progress, cancellation and atomic activation; box subscribers can join it.
func (b *Boxes) repairBinary(ctx context.Context, h *podman.Host, active string) {
	wanted, err := b.binarySum()
	if err != nil {
		log.Printf("host binary checksum: %v", err)
		return
	}
	if active == wanted || ctx.Err() != nil {
		return
	}
	b.mu.Lock()
	if b.binaryRepairs[h] {
		b.mu.Unlock()
		return
	}
	if b.binaryRepairs == nil {
		b.binaryRepairs = make(map[*podman.Host]bool)
	}
	b.binaryRepairs[h] = true
	b.mu.Unlock()
	go func() {
		defer func() { b.mu.Lock(); delete(b.binaryRepairs, h); b.mu.Unlock() }()
		if err := b.installBinary(ctx, h); err != nil && ctx.Err() == nil {
			log.Printf("host binary convergence: %v", err)
		}
	}()
}
