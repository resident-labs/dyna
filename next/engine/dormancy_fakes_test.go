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
