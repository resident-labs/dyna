package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

// ErrAgyArch says the program on the source cannot run on the target, so it was not copied. It is a fact about the host, not a fault.
var ErrAgyArch = errors.New("agy binary architecture differs")

// SeedAgy is the owner's `dyna agy seed`: it copies the signed-in agy account folder, and the program when the host has none, from
// engine.AgySource to the target, as an opaque archive carried over the two hosts' drivers (as StageBinary sends the engine's own
// binary). The target's account folder is left alone unless replace says so, and a target that has what is wanted is left alone.
// The caller has decided that the host may be seeded.
func (b *Boxes) SeedAgy(ctx context.Context, name contract.HostName, replace bool) error {
	b.mu.Lock()
	if b.agySeeding[name] {
		b.mu.Unlock()
		return fmt.Errorf("host %s is being seeded already", name)
	}
	if b.agySeeding == nil {
		b.agySeeding = map[contract.HostName]bool{}
	}
	b.agySeeding[name] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.agySeeding, name)
		b.mu.Unlock()
	}()
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
	pack, err := source.PackAgy(ctx, b.Layout, program, account)
	if err != nil {
		return err
	}
	sum := ""
	if program {
		sum = have.BinarySum
	}
	return target.UnpackAgy(ctx, b.Layout, pack, sum, replace)
}
