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
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
)

const (
	engineSocket = "/run/dyna/engine/engine.sock"
	piDir        = "/box/pi"
)

func modelArgs(model contract.ModelChoice) ([]string, error) {
	switch model.Provider {
	case contract.ModelProviderAnthropic, contract.ModelProviderOpenAI:
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
	setup := func(ctx context.Context) error { return setupRepos(ctx, "/box/work", hello.Repos, runGit) }
	setupErr := setup(ctx)
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
	transcript := NewTranscript(client, sessions)
	transcript.syncLogged(ctx, h.SessionFile())
	ticks := time.NewTicker(transcriptInterval)
	defer ticks.Stop()
	go transcript.Run(ctx, ticks.C, h.SessionFile)
	runner := &Runner{Harness: h, Sink: endedSink{turnSink: client, transcript: transcript, file: h.SessionFile}, Setup: setup, setupErr: setupErr}
	go func() {
		errs <- client.Channel(ctx, func(message contract.BoxMessage) error {
			if _, ok := message.Body.(contract.Archive); ok {
				return closeBox(ctx, h, transcript, h.SessionFile(), func(ctx context.Context) error {
					for _, repo := range hello.Repos {
						if err := client.ArchiveDone(ctx, archiveRepo(ctx, "/box/work", hello.Session, repo, runGit)); err != nil {
							return err
						}
					}
					return nil
				})
			}
			return runner.Handle(ctx, message)
		})
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
