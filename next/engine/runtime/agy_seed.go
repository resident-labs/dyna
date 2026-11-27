package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

// ErrAgyArch says the program on the source cannot run on the target, so it was not copied. It is a fact about the host, not a fault.
var ErrAgyArch = errors.New("agy binary architecture differs")

// agyBarrierWait is how long the barrier waits for a seed's unpack to let go of the host's seed lock.
const agyBarrierWait = 20 * time.Second

// AgyBarrier says whether no unpack of a seed is running on the host: its lock is free. An unreachable host, or a lock still held after
// the wait, is an error, and a mark that waits on it stays.
func (b *Boxes) AgyBarrier(ctx context.Context, name contract.HostName) error {
	h, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, agyBarrierWait+10*time.Second)
	defer cancel()
	return h.AgyBarrier(ctx, b.Layout, agyBarrierWait)
}

// SeedAgy is the owner's `dyna agy seed`: it copies the signed-in agy account folder, and the program when the host has none, from
// engine.AgySource to the target, as an opaque archive carried over the two hosts' drivers (as StageBinary sends the engine's own
// binary). The target's account folder is left alone unless replace says so, and a target that has what is wanted is left alone.
// The caller has decided that the host may be seeded.
func (b *Boxes) SeedAgy(ctx context.Context, name contract.HostName, replace bool) error {
	source, err := b.host(ctx, engine.AgySource)
	if err != nil {
		return err
	}
	target, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	have, err := source.AgyState(ctx, b.Layout)
	if err != nil {
		return err
	}
	has, err := target.AgyState(ctx, b.Layout)
	if err != nil {
		return err
	}
	account, program := !has.HasAccount || replace, !has.HasBinary
	if !account && !program {
		return nil
	}
	if account && !have.HasAccount || program && !have.HasBinary {
		return fmt.Errorf("host %s has no agy to copy", engine.AgySource)
	}
	if program && have.Arch != has.Arch {
		return ErrAgyArch
	}
	sum := ""
	if program {
		sum = have.BinarySum
	}
	// The archive passes from one host's tar to the other's through this pipe: the engine holds no copy of it, and neither host writes it to a file.
	pr, pw := io.Pipe()
	packed := make(chan error, 1)
	go func() {
		err := source.PackAgy(ctx, b.Layout, program, account, pw)
		pw.CloseWithError(err) // a failed pack ends the stream with an error, never a clean end
		packed <- err
	}()
	unpackErr := target.UnpackAgy(ctx, b.Layout, pr, sum, replace)
	pr.CloseWithError(errors.New("the target stopped reading"))
	if err := <-packed; err != nil {
		return err
	}
	return unpackErr
}
