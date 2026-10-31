package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

func (fakeBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (unavailableBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (blockingBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (*recordingBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (checkingBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (*recoveryBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (*retryTimeoutBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (*blockingTimeoutBoxes) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (cancelOnRemove) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (*heldParentBox) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (closingInterleaveBox) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (heldRemoveBox) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (*movingHost) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
func (*retryHost) MaintainHost(context.Context, contract.HostName) ([]contract.RoomShared, error) {
	return nil, nil
}
