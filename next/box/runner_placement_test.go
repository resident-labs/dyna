package box_test

import (
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestRunnerPlacement(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeSmall)
	request := box.Box{ID: "new", Image: box.RunnerImage, Size: size}
	for _, tt := range []struct {
		name       string
		big, small *float64
		readySmall bool
		want       contract.HostName
	}{
		{"saturated big", ptr(48), ptr(0), false, "small"},
		{"missing heartbeat", nil, nil, false, "big"},
		{"image ready preference", ptr(0), ptr(0), true, "small"},
		{"all saturated", ptr(48), ptr(8), false, "big"},
		{"idle cores ranking", ptr(47), ptr(0), false, "small"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hosts := []box.Host{
				{Name: "big", State: box.HostReady, CPUs: 48, CPUUsed: tt.big, MemBytes: 128 * box.GiB, DiskBytes: 1000 * box.GiB, CIScale: 1},
				{Name: "small", State: box.HostReady, CPUs: 8, CPUUsed: tt.small, MemBytes: 32 * box.GiB, DiskBytes: 1000 * box.GiB, CIScale: 1},
			}
			ready := hosts[:1]
			if tt.readySmall {
				ready = hosts[1:]
			}
			if p := box.AdmitRunner(hosts, ready, nil, request); p.Host != tt.want {
				t.Fatalf("got %+v, want %s", p, tt.want)
			}
			if tt.name == "saturated big" || tt.name == "idle cores ranking" {
				if p := box.Admit(hosts, nil, request); p.Host != "small" {
					t.Fatalf("real idle cores ranking: %+v", p)
				}
				if p := box.AdmitRunner(hosts, hosts, nil, request); p.Host != "small" {
					t.Fatalf("ready hosts ranking: %+v", p)
				}
			}
			session := request
			session.Image = "session"
			if p := box.Admit(hosts, nil, session); p.Host != "big" {
				t.Fatalf("session placement changed: %+v", p)
			}
		})
	}
}

func ptr(v float64) *float64 { return &v }
