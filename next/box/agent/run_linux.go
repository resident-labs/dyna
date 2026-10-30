//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
)

const (
	engineSocket = "/run/dyna/engine/engine.sock"
	piDir        = "/box/pi"
)

func modelArgs(model contract.ModelChoice) ([]string, error) {
	switch model.Provider {
	case contract.ProviderAnthropic, contract.ProviderOpenAI:
	default:
		return nil, fmt.Errorf("unsupported model provider %q", model.Provider)
	}
	switch model.Thinking {
	case contract.ThinkingOff, contract.ThinkingMinimal, contract.ThinkingLow, contract.ThinkingMedium, contract.ThinkingHigh, contract.ThinkingXHigh:
	default:
		return nil, fmt.Errorf("unsupported thinking level %q", model.Thinking)
	}
	if model.ID == "" {
		return nil, fmt.Errorf("model ID is empty")
	}
	return []string{"--provider", string(model.Provider), "--model", model.ID, "--thinking", string(model.Thinking)}, nil
}

func upgrade() error {
	path := "/opt/dyna/bin/current/dyna"
	return syscall.Exec(path, []string{path, "box-agent"}, os.Environ())
}

// Run connects the box, serves its model route, and runs the Pi session.
func Run(ctx context.Context, version string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client := NewClient(engineSocket)
	errs := make(chan error, 3)
	go client.Run(ctx)
	hello, err := client.Hello(ctx, version)
	if err != nil {
		return err
	}
	if hello.Upgrade != "" {
		return upgrade()
	}
	args, err := modelArgs(hello.Model)
	if err != nil {
		return err
	}
	sessions := filepath.Join(piDir, "sessions")
	if err = os.MkdirAll(sessions, 0700); err != nil {
		return err
	}
	models, err := json.Marshal(Models())
	if err != nil {
		return err
	}
	modelsPath := filepath.Join(piDir, "models.json")
	if err = os.WriteFile(modelsPath, models, 0600); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:7781")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: ModelRoute(engineSocket)}
	defer server.Close()
	go func() { errs <- server.Serve(listener) }()
	args = append(args, "--mode", "rpc", "--models-file", modelsPath, "--session-dir", sessions, "--continue")
	h, err := pi.Open(ctx, "pi", args, "")
	if err != nil {
		return err
	}
	defer h.Close()
	runner := &Runner{Harness: h, Sink: client, Upgrade: func() error {
		if err := h.Close(); err != nil {
			return err
		}
		return upgrade()
	}}
	go func() {
		errs <- client.Channel(ctx, func(message contract.BoxMessage) error { return runner.Handle(ctx, message) })
	}()
	select {
	case err := <-errs:
		return err
	case err := <-runner.Errors():
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
