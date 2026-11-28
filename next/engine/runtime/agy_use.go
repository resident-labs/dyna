package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

// The hosts side of `dyna agy use`: which hosts run agy, whether each answers, and the token swap on one. The token is a stored file on
// the engine host that is streamed to the host as it is, twice (the second copy is what the host compares the installed file with),
// and never parsed, logged or hashed here.

var _ engine.AgyHosts = (*Boxes)(nil)

// AgyHostNames lists the hosts that run agy, the engine's own (engine.AgySource) first, whether or not the list has marked it yet.
func (b *Boxes) AgyHostNames(ctx context.Context) ([]contract.HostName, error) {
	hosts, err := b.Store.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	names := []contract.HostName{engine.AgySource}
	for _, h := range hosts {
		if h.Agy && h.State != box.HostRemoved && h.Name != engine.AgySource && !slices.Contains(names, h.Name) {
			names = append(names, h.Name)
		}
	}
	return names, nil
}

// AgyHostAnswers says nil when the host answers and has an agy account folder to put a token in.
func (b *Boxes) AgyHostAnswers(ctx context.Context, name contract.HostName) error {
	host, err := b.host(ctx, name)
	if err != nil {
		return err
	}
	state, err := host.AgyState(ctx, b.Layout)
	if err != nil {
		return err
	}
	if !state.HasAccount {
		return errors.New("the host has no agy account folder; seed it first")
	}
	return nil
}

// twice reads the stored token file two times in a row, as its size says, for the host's install and its check.
func twice(path string) (io.Reader, int64, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, errors.New("the stored token file is missing")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		f.Close()
		return nil, 0, nil, errors.New("the stored token file is missing")
	}
	size := info.Size()
	return io.MultiReader(io.NewSectionReader(f, 0, size), io.NewSectionReader(f, 0, size)), size, func() { f.Close() }, nil
}

// AgyTokenIs says whether the host's token file is the same bytes as the stored file.
func (b *Boxes) AgyTokenIs(ctx context.Context, name contract.HostName, stored string) (bool, error) {
	host, err := b.host(ctx, name)
	if err != nil {
		return false, err
	}
	r, size, closeFile, err := twice(stored)
	if err != nil {
		return false, err
	}
	defer closeFile()
	return host.AgyTokenIs(ctx, b.Layout, io.LimitReader(r, size))
}

// AgyInstall swaps the host's token for the stored file, after copying the current one to takeback when that is not empty, and returns
// how many agy servers it ended.
func (b *Boxes) AgyInstall(ctx context.Context, name contract.HostName, stored, takeback string) (int, error) {
	host, err := b.host(ctx, name)
	if err != nil {
		return 0, err
	}
	r, size, closeFile, err := twice(stored)
	if err != nil {
		return 0, err
	}
	defer closeFile()
	if takeback == "" {
		takeback = "-"
	}
	return host.InstallAgyToken(ctx, b.Layout, takeback, size, r)
}
