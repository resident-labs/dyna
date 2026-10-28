package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/gpu"
)

// GPU memory near exhaustion killed this machine's desktop once (D-352): the
// kernel logged NVIDIA out-of-memory errors while Dyna computers rendered on
// the GPU behind the owner's display. The doctor reads GPU memory from the
// cheapest per-vendor source: sysfs mem_info_vram for amdgpu, nvidia-smi for
// the proprietary NVIDIA driver. Anything else is skipped, and said so.

// maxGPUMemory is the used share past which a GPU is unhealthy, matching the
// system-memory limit.
const maxGPUMemory = 85.0

// GPUReading is one GPU's memory.
type GPUReading struct {
	// GPU names the device for the owner, such as "nvidia-smi GPU 0" or
	// "card1 (nvidia)".
	GPU string
	// Used and Total are bytes; a zero Total was never reported.
	Used, Total uint64
}

// sysfsDRMClass is the directory the sysfs readings come from.
const sysfsDRMClass = "/sys/class/drm"

// readGPUs reads every GPU's memory from cheap kernel or driver sources. A
// GPU with no readable source is left out. The note explains why GPU memory
// was not checked at all, and is empty when it was.
func readGPUs() (readings []GPUReading, note string) {
	cards := gpu.Read(sysfsDRMClass).Cards
	for _, card := range cards {
		used, uok := readUint(filepath.Join(sysfsDRMClass, card.Name, "device", "mem_info_vram_used"))
		total, tok := readUint(filepath.Join(sysfsDRMClass, card.Name, "device", "mem_info_vram_total"))
		if !uok || !tok || total == 0 {
			continue
		}
		readings = append(readings, GPUReading{GPU: card.Name + " (" + card.Driver + ")", Used: used, Total: total})
	}
	if len(cards) > 0 {
		nvidia := nvidiaSmiMemory()
		for _, r := range nvidia {
			readings = append(readings, r)
		}
	}
	if len(readings) == 0 && len(cards) > 0 {
		note = "GPU memory not checked (no sysfs mem_info_vram or nvidia-smi)"
	}
	return readings, note
}

func readUint(path string) (uint64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// nvidiaSmiMemory reads the proprietary driver's memory counts, in bytes.
func nvidiaSmiMemory() []GPUReading {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--query-gpu=index,name,memory.used,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	var readings []GPUReading
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) != 4 {
			continue
		}
		used, uerr := strconv.ParseUint(strings.TrimSpace(fields[2]), 10, 64)
		total, terr := strconv.ParseUint(strings.TrimSpace(fields[3]), 10, 64)
		if uerr != nil || terr != nil || total == 0 {
			continue
		}
		readings = append(readings, GPUReading{
			GPU:   fmt.Sprintf("nvidia-smi GPU %s %s", strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])),
			Used:  used << 20,
			Total: total << 20,
		})
	}
	return readings
}

// ReadGPUs exposes the real reader to the doctor CLI, the one production
// caller that installs it on Samples.
func ReadGPUs() ([]GPUReading, string) { return readGPUs() }

// gpuHealth turns the readings into findings and a summary symptom. A GPU
// over the limit is a finding; the note passes through unchanged.
func gpuHealth(readings []GPUReading, note string) (findings []Finding, symptom string) {
	for _, r := range readings {
		if r.Total == 0 || float64(r.Used)/float64(r.Total)*100 < maxGPUMemory {
			continue
		}
		findings = append(findings, Finding{
			Cause:    fmt.Sprintf("%s memory is %.0f%% full", r.GPU, float64(r.Used)/float64(r.Total)*100),
			Evidence: fmt.Sprintf("%s of %s used; past %d%% the driver starts failing allocations", bytes(r.Used), bytes(r.Total), int(maxGPUMemory)),
			Command:  "dyna computer status",
		})
		if symptom == "" {
			symptom = fmt.Sprintf("gpu memory %.0f%% full", float64(r.Used)/float64(r.Total)*100)
		} else {
			symptom += fmt.Sprintf(", %.0f%%", float64(r.Used)/float64(r.Total)*100)
		}
	}
	return findings, symptom
}
