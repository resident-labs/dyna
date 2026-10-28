// Package gpu reads GPU identities from the kernel's DRM sysfs tree.
package gpu

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Inventory is the GPU hardware listed by one DRM sysfs directory.
type Inventory struct {
	Cards       []Card
	RenderNodes []RenderNode
}

// Card is one DRM card and its bound kernel driver.
type Card struct {
	Name string
	// Driver is empty when no kernel driver is bound.
	Driver string
}

// RenderNode is one DRM render node and the hardware identity apps need.
type RenderNode struct {
	// Node is the device path, such as /dev/dri/renderD128.
	Node string
	// Card is the card that shares the render node's PCI device; empty when
	// sysfs does not identify one.
	Card string
	// PCI is the Mesa DRI_PRIME target; empty when the bus address is unknown.
	PCI string
	// Vendor is the PCI vendor ID, or zero when sysfs does not provide it.
	Vendor uint32
	// Driver is the bound kernel driver name; empty when unbound.
	Driver string
}

// Read inventories the card and render-node entries in a DRM sysfs class
// directory. Connector entries are not cards. Missing or malformed optional
// identity files leave the corresponding fields at their zero values.
func Read(class string) Inventory {
	entries, err := os.ReadDir(class)
	if err != nil {
		return Inventory{}
	}

	var inventory Inventory
	cardDevices := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !isNumbered(name, "card") {
			continue
		}
		device := filepath.Join(class, name, "device")
		inventory.Cards = append(inventory.Cards, Card{Name: name, Driver: readDriver(device)})
		if target, err := filepath.EvalSymlinks(device); err == nil {
			if _, exists := cardDevices[target]; !exists {
				cardDevices[target] = name
			}
		}
	}

	for _, entry := range entries {
		name := entry.Name()
		if !isNumbered(name, "renderD") {
			continue
		}
		device := filepath.Join(class, name, "device")
		node := RenderNode{
			Node:   filepath.Join("/dev/dri", name),
			Vendor: readVendor(filepath.Join(device, "vendor")),
			Driver: readDriver(device),
		}
		if target, err := filepath.EvalSymlinks(device); err == nil {
			node.PCI = driPrimeID(filepath.Base(target))
			node.Card = cardDevices[target]
		}
		inventory.RenderNodes = append(inventory.RenderNodes, node)
	}
	sort.Slice(inventory.RenderNodes, func(i, j int) bool {
		mi, _ := strconv.Atoi(strings.TrimPrefix(inventory.RenderNodes[i].Node, "renderD"))
		mj, _ := strconv.Atoi(strings.TrimPrefix(inventory.RenderNodes[j].Node, "renderD"))
		return mi < mj
	})
	return inventory
}

func isNumbered(name, prefix string) bool {
	digits := strings.TrimPrefix(name, prefix)
	if digits == name {
		return false
	}
	_, err := strconv.Atoi(digits)
	return err == nil
}

func readDriver(device string) string {
	driver, err := os.Readlink(filepath.Join(device, "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(driver)
}

func readVendor(path string) uint32 {
	contents, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	vendor, err := strconv.ParseUint(strings.TrimSpace(string(contents)), 0, 32)
	if err != nil {
		return 0
	}
	return uint32(vendor)
}

func driPrimeID(address string) string {
	if address == "" {
		return ""
	}
	return "pci-" + strings.NewReplacer(":", "_", ".", "_").Replace(address)
}
