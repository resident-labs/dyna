package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

func (fakeBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (unavailableBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (blockingBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*recordingBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (checkingBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*recoveryBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*retryTimeoutBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*blockingTimeoutBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (cancelOnRemove) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*heldParentBox) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (closingInterleaveBox) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (heldRemoveBox) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*movingHost) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*retryHost) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
