package box_test

import (
	"fmt"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"

	"testing"
)

func TestCIScaleAdmission(t *testing.T) {
	for _, scale := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			h := box.Host{Name: "h", State: box.HostReady, CPUs: 64, MemBytes: 32 * box.GiB, DiskBytes: 10000 * box.GiB}
			h.CIScale = scale
			size, _ := box.Size(contract.BoxSizeLarge)
			request := box.Box{ID: "new", Room: "r", Image: box.RunnerImage, Size: size}
			var running []box.Box
			for i := 0; i < 2*scale; i++ {
				if p := box.Admit([]box.Host{h}, running, request); p.Host != "h" {
					t.Fatalf("runner %d: %+v", i, p)
				}
				b := request
				b.ID = contract.BoxID(fmt.Sprint(i))
				b.Host = "h"
				b.State = contract.BoxReady
				running = append(running, b)
			}
			if p := box.Admit([]box.Host{h}, running, request); p.Reason != box.PendingMemory {
				t.Fatalf("reservation limit: %+v", p)
			}
			h.MemUsed = h.MemBytes - h.MemBytes/5
			if p := box.Admit([]box.Host{h}, nil, request); p.Host != "" {
				t.Fatal("80 percent memory guard bypassed")
			}
			h.MemUsed = 0
			request.Image = "session"
			// Sessions retain their 4x memory budget, whatever CI capacity says.
			running = nil
			for i := 0; i < 8; i++ {
				b := request
				b.ID = contract.BoxID(fmt.Sprint(i))
				b.Host = "h"
				b.State = contract.BoxReady
				running = append(running, b)
			}
			if p := box.Admit([]box.Host{h}, running, request); p.Reason != box.PendingMemory {
				t.Fatalf("session budget changed: %+v", p)
			}
		})
	}
}

func TestCIScaleCPUBudget(t *testing.T) {
	for _, scale := range []int{1, 2, 3, 4} {
		h := box.Host{Name: "h", State: box.HostReady, CPUs: 8, MemBytes: 1024 * box.GiB, DiskBytes: 10000 * box.GiB, CIScale: scale}
		size, _ := box.Size(contract.BoxSizeLarge)
		req := box.Box{ID: "new", Image: box.RunnerImage, Size: size}
		var running []box.Box
		for i := 0; i < 2*scale; i++ {
			if got := box.Admit([]box.Host{h}, running, req); got.Host != "h" {
				t.Fatalf("scale %d runner %d: %+v", scale, i, got)
			}
			b := req
			b.ID = contract.BoxID(fmt.Sprint(i))
			b.Host = "h"
			b.State = contract.BoxReady
			running = append(running, b)
		}
		if got := box.Admit([]box.Host{h}, running, req); got.Reason != box.PendingCPU {
			t.Fatalf("scale %d CPU cap: %+v", scale, got)
		}
		req.Image = "session"
		running = nil
		for i := 0; i < 4; i++ {
			b := req
			b.ID = contract.BoxID(fmt.Sprint(i))
			b.Host = "h"
			b.State = contract.BoxReady
			running = append(running, b)
		}
		if got := box.Admit([]box.Host{h}, running, req); got.Reason != box.PendingCPU {
			t.Fatalf("scale %d changed session CPU: %+v", scale, got)
		}
	}
}
