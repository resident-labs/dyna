package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestDeviceListMarksOnlyTheCaller(t *testing.T) {
	s, _, _, ctx := setup(t)
	now := time.Unix(1000, 0)
	ids := []string{}
	for _, name := range []string{"Phone", "Laptop"} {
		code, err := s.store.NewPairCode(ctx, []contract.Scope{"admin"}, now)
		if err != nil {
			t.Fatal(err)
		}
		_, device, err := s.store.RedeemPairCode(ctx, code, name, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, device.ID)
	}
	list, e := s.DeviceList(ctx, contract.Principal{Kind: contract.PrincipalOwner, Device: contract.DeviceID(ids[1])}, contract.DeviceListRequest{})
	if e != nil {
		t.Fatal(e)
	}
	for _, device := range list.Devices {
		if device.Current != (device.ID == ids[1]) {
			t.Errorf("%s: current is %v", device.Name, device.Current)
		}
	}
}
