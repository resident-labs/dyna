//go:build linux

package computers

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"dyna-agent/internal/settings"
)

// The render node a Dyna computer renders on (D-352). Computers default away
// from the GPU that drives the owner's display, so heavy agent apps cannot
// exhaust the memory behind the owner's desktop. The choice comes from
// /sys/class/drm on any Linux distro, whatever the GPU vendor is.

// sysfsDRMClass is the directory the render-node choice reads.
const sysfsDRMClass = "/sys/class/drm"

// nvidiaVendor is the PCI vendor id of NVIDIA, the one vendor whose
// proprietary stack ignores Mesa's DRI_PRIME and needs GLVND variables.
const nvidiaVendor = 0x10de

// nvidiaEGLVendorFile is glvnd's standard NVIDIA EGL vendor manifest.
const nvidiaEGLVendorFile = "/usr/share/glvnd/egl_vendor.d/10_nvidia.json"

// renderGPU is one render node and what apps need to use it.
type renderGPU struct {
	// Node is the device path, /dev/dri/renderD128.
	Node string
	// PCI is the Mesa DRI_PRIME target such as pci-0000_02_00_0; empty when
	// the bus address is unknown.
	PCI string
	// Vendor is the PCI vendor id: 0x1002 AMD, 0x10de NVIDIA, 0x8086 Intel.
	Vendor uint32
	// Driver is the bound kernel driver name; empty when unbound.
	Driver string
}

// pickRenderGPU resolves the computers.renderNode setting against one
// /sys/class/drm directory. The value "auto", an empty value, or a path that
// names no listed render node picks the first node whose card has no
// connected connector, so it drives no display; when every card drives one,
// or no card is known, the first node stays, the behavior before D-352.
// A path that names a listed /dev/dri/renderD<digits> node wins over auto,
// whatever card it belongs to. Only the listing decides: the same sysfs tree
// that startDesktop will render from, with no look at /dev/dri, so a stale
// setting on a machine whose nodes were renumbered falls back to auto.
func pickRenderGPU(setting, class string) (renderGPU, bool) {
	setting = strings.TrimSpace(setting)
	gpus := listRenderGPUs(class)
	if len(gpus) == 0 {
		return renderGPU{}, false
	}
	if name := filepath.Base(setting); strings.HasPrefix(setting, "/dev/dri/") && isRenderNodeName(name) {
		for _, gpu := range gpus {
			if filepath.Base(gpu.Node) == name || gpu.Node == setting {
				return gpu, true
			}
		}
	}
	display := displayCards(class)
	for _, gpu := range gpus {
		if !display[cardOfNode(gpu, class)] {
			return gpu, true
		}
	}
	return gpus[0], true
}

// listRenderGPUs reads the render nodes of a /sys/class/drm directory,
// ordered by node number.
func listRenderGPUs(class string) []renderGPU {
	entries, err := os.ReadDir(class)
	if err != nil {
		return nil
	}
	var gpus []renderGPU
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "renderD") || !isRenderNodeName(name) {
			continue
		}
		dev := filepath.Join(class, name, "device")
		gpu := renderGPU{Node: filepath.Join("/dev/dri", name)}
		if target, err := filepath.EvalSymlinks(dev); err == nil {
			gpu.PCI = driPrimeID(filepath.Base(target))
		}
		gpu.Vendor = readVendorID(filepath.Join(dev, "vendor"))
		if driver, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			gpu.Driver = filepath.Base(driver)
		}
		gpus = append(gpus, gpu)
	}
	sort.Slice(gpus, func(i, j int) bool {
		mi, _ := strconv.Atoi(strings.TrimPrefix(gpus[i].Node, "renderD"))
		mj, _ := strconv.Atoi(strings.TrimPrefix(gpus[j].Node, "renderD"))
		return mi < mj
	})
	return gpus
}

// isRenderNodeName reports whether name is a render node entry such as
// renderD128.
func isRenderNodeName(name string) bool {
	digits := strings.TrimPrefix(name, "renderD")
	if digits == name {
		return false
	}
	_, err := strconv.Atoi(digits)
	return err == nil
}

// driPrimeID turns a PCI address such as 0000:02:00.0 into the DRI_PRIME
// target Mesa names, pci-0000_02_00_0. An empty answer leaves DRI_PRIME unset.
func driPrimeID(address string) string {
	if address == "" {
		return ""
	}
	return "pci-" + strings.NewReplacer(":", "_", ".", "_").Replace(address)
}

// readVendorID reads a sysfs vendor file such as "0x1002\n".
func readVendorID(path string) uint32 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 0, 32)
	if err != nil {
		return 0
	}
	return uint32(v)
}

// displayCards maps each card name of a /sys/class/drm directory that has a
// connector with a monitor connected. A connected connector is the cheap
// kernel signal that the card can drive the owner's display; the enabled
// state flips when the display sleeps, so it is not used.
func displayCards(class string) map[string]bool {
	entries, err := os.ReadDir(class)
	if err != nil {
		return nil
	}
	display := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		card, _, found := strings.Cut(name, "-")
		if !found || !isCardName(card) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(class, name, "status"))
		if err != nil || strings.TrimSpace(string(b)) != "connected" {
			continue
		}
		display[card] = true
	}
	return display
}

// isCardName reports whether name is a card entry such as card1, not a
// connector such as card1-DP-1.
func isCardName(name string) bool {
	digits := strings.TrimPrefix(name, "card")
	if digits == name {
		return false
	}
	_, err := strconv.Atoi(digits)
	return err == nil
}

// cardOfNode names the card a render node belongs to, by the PCI device they
// share; empty when sysfs does not say.
func cardOfNode(gpu renderGPU, class string) string {
	nodeDev, err := filepath.EvalSymlinks(filepath.Join(class, filepath.Base(gpu.Node), "device"))
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(class)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		card := e.Name()
		if !isCardName(card) {
			continue
		}
		if dev, err := filepath.EvalSymlinks(filepath.Join(class, card, "device")); err == nil && dev == nodeDev {
			return card
		}
	}
	return ""
}

// configuredRenderGPU resolves the computers.renderNode setting against the
// machine's real /sys/class/drm. A settings error leaves the auto choice.
func configuredRenderGPU() renderGPU {
	setting := "auto"
	if v, err := settings.Load(""); err == nil {
		setting = v.ComputersRenderNode
	}
	gpu, _ := pickRenderGPU(setting, sysfsDRMClass)
	return gpu
}

// gpuEnvValues are the environment variables that point an app at one GPU.
func gpuEnvValues(gpu renderGPU) map[string]string {
	if gpu.Node == "" {
		return nil
	}
	if gpu.Vendor == nvidiaVendor || gpu.Driver == "nvidia" {
		// The proprietary NVIDIA stack ignores DRI_PRIME; GLVND picks the
		// vendor for GLX and EGL instead.
		values := map[string]string{"__GLX_VENDOR_LIBRARY_NAME": "nvidia"}
		if _, err := os.Stat(nvidiaEGLVendorFile); err == nil {
			values["__EGL_VENDOR_LIBRARY_FILENAMES"] = nvidiaEGLVendorFile
		}
		return values
	}
	if gpu.PCI != "" {
		return map[string]string{"DRI_PRIME": gpu.PCI}
	}
	return nil
}

// gpuAppEnvironment moves app environment base onto one GPU. Owner values for
// the same variables are dropped first, so a stale DRI_PRIME from the owner's
// shell cannot pull apps back onto the display GPU.
func gpuAppEnvironment(base []string, gpu renderGPU) []string {
	drop := map[string]bool{"DRI_PRIME": true, "__GLX_VENDOR_LIBRARY_NAME": true, "__EGL_VENDOR_LIBRARY_FILENAMES": true}
	out := make([]string, 0, len(base)+3)
	for _, line := range base {
		k, _, _ := strings.Cut(line, "=")
		if drop[k] {
			continue
		}
		out = append(out, line)
	}
	for k, v := range gpuEnvValues(gpu) {
		out = append(out, k+"="+v)
	}
	return out
}
