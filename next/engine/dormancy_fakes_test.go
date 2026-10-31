package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

func (checkingBoxes) Start(context.Context, contract.BoxID) error         { return nil }
func (checkingBoxes) Stop(context.Context, contract.BoxID) error          { return nil }
func (*recoveryBoxes) Start(context.Context, contract.BoxID) error        { return nil }
func (*recoveryBoxes) Stop(context.Context, contract.BoxID) error         { return nil }
func (*retryTimeoutBoxes) Start(context.Context, contract.BoxID) error    { return nil }
func (*retryTimeoutBoxes) Stop(context.Context, contract.BoxID) error     { return nil }
func (*blockingTimeoutBoxes) Start(context.Context, contract.BoxID) error { return nil }
func (*blockingTimeoutBoxes) Stop(context.Context, contract.BoxID) error  { return nil }
func (unavailableBoxes) Start(context.Context, contract.BoxID) error      { return nil }
func (unavailableBoxes) Stop(context.Context, contract.BoxID) error       { return nil }
func (blockingBoxes) Start(context.Context, contract.BoxID) error         { return nil }
func (blockingBoxes) Stop(context.Context, contract.BoxID) error          { return nil }
func (cancelOnRemove) Start(context.Context, contract.BoxID) error        { return nil }
func (cancelOnRemove) Stop(context.Context, contract.BoxID) error         { return nil }
func (*recordingBoxes) Start(context.Context, contract.BoxID) error       { return nil }
func (*recordingBoxes) Stop(context.Context, contract.BoxID) error        { return nil }
func (*heldParentBox) Start(context.Context, contract.BoxID) error        { return nil }
func (*heldParentBox) Stop(context.Context, contract.BoxID) error         { return nil }
func (closingInterleaveBox) Start(context.Context, contract.BoxID) error  { return nil }
func (closingInterleaveBox) Stop(context.Context, contract.BoxID) error   { return nil }
func (heldRemoveBox) Start(context.Context, contract.BoxID) error         { return nil }
func (heldRemoveBox) Stop(context.Context, contract.BoxID) error          { return nil }

func (checkingBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (*recoveryBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (*retryTimeoutBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (*blockingTimeoutBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (unavailableBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (blockingBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (cancelOnRemove) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (*recordingBoxes) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (*heldParentBox) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (closingInterleaveBox) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (heldRemoveBox) Recreate(context.Context, BoxSpec, string, string) error { return nil }

func (checkingBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (*recoveryBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (*blockingTimeoutBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (unavailableBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (blockingBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (cancelOnRemove) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (*recordingBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (*heldParentBox) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (closingInterleaveBox) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}

func (heldRemoveBox) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}
