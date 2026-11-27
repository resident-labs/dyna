package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

// ErrAgyArch says the program on the source cannot run on the target, so it was not copied. It is a fact about the host, not a fault.
var ErrAgyArch = errors.New("agy binary architecture differs")

// SeededError says the host has an account folder already, and what the owner does to seed it again.
type SeededError struct {
	Host contract.HostName
	Path string
}

func (e SeededError) Error() string {
	return fmt.Sprintf("host %s is seeded already, and a folder is never replaced in place; to seed it again, close its agy sessions and every agy process using the old folder, remove %s on that host, and seed again", e.Host, e.Path)
}

// ProgramError says the host's agy program path holds something that is not an executable file, so nothing was published.
type ProgramError struct {
	Host contract.HostName
	Path string
}

func (e ProgramError) Error() string {
	return fmt.Sprintf("the agy program at %s on host %s is not an executable file, or went missing during the seed; remove it on that host and seed again", e.Path, e.Host)
}

// SeedAgy is the owner's `dyna agy seed`: it copies the signed-in agy account folder, and the program when the host has none, from
// engine.AgySource to the target, as an opaque archive streamed between the two hosts' drivers, with no spool and no copy in memory. A host
// that has an account folder is refused here, and again by the target's own script under its lock, so of two seeds at once exactly one
// publishes. The caller has decided that the host may be seeded.
func (b *Boxes) SeedAgy(ctx context.Context, name contract.HostName) error {
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
	seeded := SeededError{Host: name, Path: b.Layout.HostAgyAccount()}
	if has.HasAccount {
		return seeded
	}
	program := !has.HasBinary
	if !have.HasAccount || program && !have.HasBinary {
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
		err := source.PackAgy(ctx, b.Layout, program, true, pw)
		pw.CloseWithError(err) // a failed pack ends the stream with an error, never a clean end
		packed <- err
	}()
	unpackErr := target.UnpackAgy(ctx, b.Layout, pr, sum)
	pr.CloseWithError(errors.New("the target stopped reading"))
	packErr := <-packed
	// A losing seed stops reading, so the source's pipe breaks: the target's answer is the result, not that.
	if errors.Is(unpackErr, podman.ErrAgySeeded) {
		return seeded
	}
	if errors.Is(unpackErr, podman.ErrAgyProgram) {
		return ProgramError{Host: name, Path: b.Layout.HostAgyBinary()}
	}
	if packErr != nil {
		return packErr
	}
	return unpackErr
}
