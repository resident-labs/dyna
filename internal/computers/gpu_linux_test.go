//go:build linux

package computers

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGPU makes one fake GPU in a /sys/class/drm tree: a card directory, its
// render node, the PCI device they share, and connector status files.
func writeGPU(t *testing.T, class, card, node, pci string, vendor string, connectors map[string]string) {
	t.Helper()
	dev := filepath.Join(class, "devices", pci)
	if err := os.MkdirAll(dev, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dev, "vendor"), []byte(vendor+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(class, card), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dev, filepath.Join(class, card, "device")); err != nil {
		t.Fatal(err)
	}
	for name, status := range connectors {
		dir := filepath.Join(class, card+"-"+name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(class, node), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dev, filepath.Join(class, node, "device")); err != nil {
		t.Fatal(err)
	}
}

// auto must keep Dyna computers off the GPU that drives the owner's display:
// the NVIDIA card with the connected monitor is skipped for the AMD card
// without one. An explicit setting wins, and when every card drives a display
// the first node stays, as before (D-352).
func TestPickRenderGPUBypassesDisplayCard(t *testing.T) {
	class := t.TempDir()
	writeGPU(t, class, "card0", "renderD128", "0000:01:00.0", "0x10de", map[string]string{"DP-1": "connected"})
	writeGPU(t, class, "card1", "renderD129", "0000:02:00.0", "0x1002", map[string]string{"DP-1": "disconnected"})

	gpu, ok := pickRenderGPU("auto", class)
	if !ok {
		t.Fatal("auto found no render node")
	}
	if gpu.Node != "/dev/dri/renderD129" {
		t.Fatalf("auto chose %s; want /dev/dri/renderD129, the card without the display", gpu.Node)
	}
	if gpu.PCI != "pci-0000_02_00_0" {
		t.Fatalf("PCI id %q; want pci-0000_02_00_0 for DRI_PRIME", gpu.PCI)
	}
	if gpu.Vendor != 0x1002 {
		t.Fatalf("vendor %#x; want 0x1002", gpu.Vendor)
	}

	gpu, ok = pickRenderGPU("auto\n", class)
	if !ok || gpu.Node != "/dev/dri/renderD129" {
		t.Fatalf("untrimmed auto chose %+v; want renderD129", gpu)
	}

	// An explicit setting names the node even when it drives the display.
	gpu, ok = pickRenderGPU("/dev/dri/renderD128", class)
	if !ok || gpu.Node != "/dev/dri/renderD128" || gpu.Vendor != 0x10de {
		t.Fatalf("explicit setting chose %+v; want renderD128 on the NVIDIA card", gpu)
	}

	// A path that is not a render node, or one that no listed card owns
	// (a stale number after a reboot), falls back to auto instead of leaving
	// the computer without a GPU.
	gpu, ok = pickRenderGPU("/dev/dri/card0", class)
	if !ok || gpu.Node != "/dev/dri/renderD129" {
		t.Fatalf("card path fell back to %+v; want renderD129", gpu)
	}
	gpu, ok = pickRenderGPU("/dev/dri/renderD999", class)
	if !ok || gpu.Node != "/dev/dri/renderD129" {
		t.Fatalf("stale node fell back to %+v; want renderD129", gpu)
	}

	// Both cards drive a display: keep the first node, the old behavior.
	connected := map[string]string{"HDMI-A-1": "connected"}
	writeGPU(t, class, "card2", "renderD130", "0000:03:00.0", "0x8086", connected)
	// card2's node replaces card1's in the auto choice only when card1 also
	// drives a display, which the next write makes true.
	dir := filepath.Join(class, "card1-DP-1", "status")
	if err := os.WriteFile(dir, []byte("connected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gpu, ok = pickRenderGPU("auto", class)
	if !ok || gpu.Node != "/dev/dri/renderD128" {
		t.Fatalf("all-display fallback chose %+v; want renderD128", gpu)
	}
}
