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

// A running desktop keeps the GPU it started on: apps must not follow a
// settings change made after startup, and an owner's stale DRI_PRIME must not
// pull them onto another GPU (D-352 review).
func TestAppsFollowTheRunningDesktopGPU(t *testing.T) {
	class := t.TempDir()
	writeGPU(t, class, "card0", "renderD128", "0000:01:00.0", "0x10de", map[string]string{"DP-1": "connected"})
	writeGPU(t, class, "card1", "renderD129", "0000:02:00.0", "0x1002", nil)
	old := sysfsDRMClass
	sysfsDRMClass = class
	t.Cleanup(func() { sysfsDRMClass = old })

	// auto would pick renderD129; the desktop runs on the NVIDIA node with
	// the variables it resolved at start, and apps keep them.
	env := gpuEnvForStatus(Status{State: StateRunning, RenderNode: "/dev/dri/renderD128", RenderEnv: map[string]string{"__GLX_VENDOR_LIBRARY_NAME": "nvidia"}})
	if env["__GLX_VENDOR_LIBRARY_NAME"] != "nvidia" {
		t.Fatalf("running desktop resolved to %v; want its own GLVND variables", env)
	}
	// An older daemon without stored variables still resolves by node.
	if env := gpuEnvForStatus(Status{State: StateRunning, RenderNode: "/dev/dri/renderD128"}); env["__GLX_VENDOR_LIBRARY_NAME"] != "nvidia" {
		t.Fatalf("node-only status resolved to %v; want the NVIDIA GLVND pin", env)
	}
	// A stale owner DRI_PRIME is dropped, never merged.
	merged := gpuAppEnvironment([]string{"DRI_PRIME=pci-0000_09_09_0", "HOME=/root"}, gpuByNode("/dev/dri/renderD128"))
	var prime, glx, home string
	for _, line := range merged {
		switch line {
		case "DRI_PRIME=pci-0000_09_09_0":
			prime = line
		case "__GLX_VENDOR_LIBRARY_NAME=nvidia":
			glx = line
		case "HOME=/root":
			home = line
		}
	}
	if prime != "" {
		t.Fatalf("a stale owner DRI_PRIME survived: %q", prime)
	}
	if glx == "" || home == "" {
		t.Fatalf("merged env = %v; want the GLVND pin and unrelated variables kept", merged)
	}

	// Once the desktop stops, the next start's choice applies again.
	if env := gpuEnvForStatus(Status{State: StateIdleStopped}); env["DRI_PRIME"] != "pci-0000_02_00_0" {
		t.Fatalf("stopped computer resolved to %v; want the auto choice's DRI_PRIME", env)
	}
}
