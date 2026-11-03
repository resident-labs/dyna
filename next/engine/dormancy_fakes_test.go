package engine

import (
	"context"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func (*recoveryBoxes) Start(context.Context, contract.BoxID) error { return nil }
func (*recoveryBoxes) Stop(context.Context, contract.BoxID) error  { return nil }

func (*recoveryBoxes) Recreate(context.Context, BoxSpec, string) error { return nil }

func (*recoveryBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (*recoveryBoxes) ExecArgv(context.Context, contract.HostName, contract.BoxID, ...string) ([]string, error) {
	return nil, nil
}

func (*recoveryBoxes) Inspect(_ context.Context, id contract.BoxID) (box.ContainerStatus, bool, error) {
	return box.ContainerStatus{Box: id, Running: true}, true, nil
}

func (fakeBoxes) Inspect(_ context.Context, id contract.BoxID) (box.ContainerStatus, bool, error) {
	return box.ContainerStatus{Box: id, Running: true}, true, nil
}

func (*recoveryBoxes) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: "image"}, nil
}
func (fakeBoxes) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: "image"}, nil
}
