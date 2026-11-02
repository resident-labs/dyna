package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

func (fakeBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
func (*recoveryBoxes) MaintainHost(context.Context, contract.HostName) error {
	return nil
}
