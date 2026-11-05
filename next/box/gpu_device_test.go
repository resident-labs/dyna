package box_test

import (
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestGPUDeviceIsTheAssignedCardOfABoxThatAskedForOne(t *testing.T) {
	if got := (box.Box{GPU: 0, GPUCard: 1}).GPUDevice(); got != nil {
		t.Fatalf("a box that asked for none: card %d", *got)
	}
	got := box.Box{GPU: contract.GPUGiB(4), GPUCard: 1}.GPUDevice()
	if got == nil || *got != 1 {
		t.Fatalf("a box on card 1: %v", got)
	}
	zero := box.Box{GPU: 1, GPUCard: 0}.GPUDevice()
	if zero == nil || *zero != 0 {
		t.Fatalf("a box on card 0: %v", zero)
	}
}
