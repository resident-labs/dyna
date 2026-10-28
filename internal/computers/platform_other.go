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

// configuredRenderGPU is the no-GPU answer on systems without Dyna computers.
func configuredRenderGPU() renderGPU { return renderGPU{} }

// gpuAppEnvironment leaves the environment unchanged off Linux.
func gpuAppEnvironment(base []string, _ renderGPU) []string { return base }
