package gpu

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadResolvesSharedCardAndRenderIdentity(t *testing.T) {
	class := t.TempDir()
	device := filepath.Join(class, "devices", "0000:02:00.0")
	for _, path := range []string{
		device,
		filepath.Join(class, "card1"),
		filepath.Join(class, "renderD129"),
		filepath.Join(class, "card1-DP-1"),
		filepath.Join(class, "card-not-a-number"),
		filepath.Join(class, "renderDnot-a-number"),
	} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(device, "vendor"), []byte("0x1002\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/sys/bus/pci/drivers/amdgpu", filepath.Join(device, "driver")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"card1", "renderD129"} {
		if err := os.Symlink(device, filepath.Join(class, name, "device")); err != nil {
			t.Fatal(err)
		}
	}

	got := Read(class)
	want := Inventory{
		Cards: []Card{{Name: "card1", Driver: "amdgpu"}},
		RenderNodes: []RenderNode{{
			Node:   "/dev/dri/renderD129",
			Card:   "card1",
			PCI:    "pci-0000_02_00_0",
			Vendor: 0x1002,
			Driver: "amdgpu",
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %#v; want %#v", got, want)
	}
}
