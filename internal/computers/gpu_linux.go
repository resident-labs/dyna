//go:build linux

package computers

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"dyna-agent/internal/gpu"
	"dyna-agent/internal/settings"
)

// The render node a Dyna computer renders on (D-352). Computers default away
// from the GPU that drives the owner's display, so heavy agent apps cannot
// exhaust the memory behind the owner's desktop. The choice comes from
// /sys/class/drm on any Linux distro, whatever the GPU vendor is.

// sysfsDRMClass is the directory the render-node choice reads. It is a
// var so a test can point it at a fake tree.
var sysfsDRMClass = "/sys/class/drm"

// nvidiaVendor is the PCI vendor id of NVIDIA, the one vendor whose
// proprietary stack ignores Mesa's DRI_PRIME and needs GLVND variables.
const nvidiaVendor = 0x10de

// nvidiaEGLVendorFile is glvnd's standard NVIDIA EGL vendor manifest.
const nvidiaEGLVendorFile = "/usr/share/glvnd/egl_vendor.d/10_nvidia.json"

type renderGPU = gpu.RenderNode

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
	inventory := gpu.Read(class)
	gpus := inventory.RenderNodes
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
	display := displayCards(class, inventory.Cards)
	for _, gpu := range gpus {
		if !display[gpu.Card] {
			return gpu, true
		}
	}
	return gpus[0], true
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

// displayCards maps each card name of a /sys/class/drm directory that has a
// connector with a monitor connected. A connected connector is the cheap
// kernel signal that the card can drive the owner's display; the enabled
// state flips when the display sleeps, so it is not used.
func displayCards(class string, cards []gpu.Card) map[string]bool {
	entries, err := os.ReadDir(class)
	if err != nil {
		return nil
	}
	display := map[string]bool{}
	for _, card := range cards {
		prefix := card.Name + "-"
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			status, err := os.ReadFile(filepath.Join(class, entry.Name(), "status"))
			if err == nil && strings.TrimSpace(string(status)) == "connected" {
				display[card.Name] = true
			}
		}
	}
	return display
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

// gpuByNode re-reads one render node's identity from sysfs. A running
// desktop keeps the node it started on, so its apps must resolve that node,
// not the current setting; when sysfs no longer knows the node, the bare
// path is all that remains.
func gpuByNode(node string) renderGPU {
	if name := filepath.Base(node); isRenderNodeName(name) {
		nodes := gpu.Read(sysfsDRMClass).RenderNodes
		for _, gpu := range nodes {
			if filepath.Base(gpu.Node) == name {
				return gpu
			}
		}
	}
	return renderGPU{Node: node}
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

// gpuEnvForStatus returns the GPU variables for a computer's live status:
// the variables its running desktop resolved at start, so every launch path
// agrees without re-reading sysfs; a running status from an older daemon
// without them falls back to the node lookup; anything else takes the choice
// the next start will make.
func gpuEnvForStatus(st Status) map[string]string {
	if st.State == StateRunning {
		if len(st.RenderEnv) > 0 {
			return st.RenderEnv
		}
		if st.RenderNode != "" {
			return gpuEnvValues(gpuByNode(st.RenderNode))
		}
	}
	return gpuEnvValues(configuredRenderGPU())
}

// appGPUEnv resolves the GPU variables for app environments built outside the
// daemon: the running desktop's variables when one answers, else the choice
// the next start will make. The daemon never calls this; it owns the desktop
// and uses its own gpu.
func appGPUEnv(id string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), runningCheckTimeout)
	defer cancel()
	if st, err := NewClient(id).Status(ctx); err == nil {
		return gpuEnvForStatus(st)
	}
	return gpuEnvValues(configuredRenderGPU())
}

// mergeGPUEnvironment moves app environment base onto one GPU. Owner values
// for the same variables are dropped first, so a stale DRI_PRIME from the
// owner's shell cannot pull apps back onto the display GPU.
func mergeGPUEnvironment(base []string, values map[string]string) []string {
	drop := map[string]bool{"DRI_PRIME": true, "__GLX_VENDOR_LIBRARY_NAME": true, "__EGL_VENDOR_LIBRARY_FILENAMES": true}
	out := make([]string, 0, len(base)+len(values))
	for _, line := range base {
		k, _, _ := strings.Cut(line, "=")
		if drop[k] {
			continue
		}
		out = append(out, line)
	}
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	return out
}

// gpuAppEnvironment is mergeGPUEnvironment for a chosen GPU.
func gpuAppEnvironment(base []string, gpu renderGPU) []string {
	return mergeGPUEnvironment(base, gpuEnvValues(gpu))
}
