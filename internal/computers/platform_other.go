//go:build !linux

package computers

import (
	"context"
	"fmt"
)

type SupervisorOptions struct{}

func startDaemon(ctx context.Context, exe string) error { return fmt.Errorf("computers require Linux") }
func RunDaemon(ctx context.Context, options SupervisorOptions) error {
	return fmt.Errorf("computers require Linux")
}
func Destroy(ctx context.Context, id string) error { return nil }

func LaunchChromium(ctx context.Context, id string, args []string) error {
	return fmt.Errorf("computers require Linux")
}

func ProcessStart(pid int) (string, bool) { return "", false }

// renderGPU mirrors the Linux render-node choice for cross-platform files.
type renderGPU struct {
	Node, PCI, Driver string
	Vendor            uint32
}

// appGPUEnv is the no-GPU answer on systems without Dyna computers.
func appGPUEnv(string) map[string]string { return nil }

// gpuEnvForStatus is the no-GPU answer off Linux.
func gpuEnvForStatus(st Status) map[string]string { return nil }

// mergeGPUEnvironment leaves the environment unchanged off Linux.
func mergeGPUEnvironment(base []string, _ map[string]string) []string { return base }
