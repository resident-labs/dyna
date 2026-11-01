//go:build linux

package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/resident-labs/dyna/box/computer"
	"github.com/resident-labs/dyna/box/dynamcp"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pi"
)

const (
	engineSocket = "/run/dyna/engine/engine.sock"
	piDir        = "/box/pi"
)

//go:embed call_id.ts
var callIDExtension string

func modelArgs(model contract.ModelChoice, compactTokens int) ([]string, error) {
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
	args := []string{"--provider", string(model.Provider), "--model", model.ID, "--thinking", string(model.Thinking)}
	if compactTokens > 0 {
		args = append(args, "--compaction-trigger-tokens", strconv.Itoa(compactTokens))
	}
	return args, nil
}

// Run connects the box, serves its model route, and runs the Pi session.
func Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	client := NewClient(engineSocket)
	errs := make(chan error, 4)
	states := make(chan struct{}, 1)
	var computerRunning atomic.Bool
	closeComputer, err := computer.Serve(ctx, "/run/dyna/agent.sock", errs, func(running bool) {
		computerRunning.Store(running)
		select {
		case states <- struct{}{}:
		default:
		}
	})
	if err != nil {
		return err
	}
	defer closeComputer()
	go client.Run(ctx)
	go func() {
		for {
			select {
			case <-states:
				running := computerRunning.Load()
				state := contract.ComputerStopped
				if running {
					state = contract.ComputerRunning
				}
				body, _ := json.Marshal(contract.ComputerChangedRequest{To: state})
				_, _ = client.call(ctx, contract.MethodComputerChanged, body)
			case <-ctx.Done():
				return
			}
		}
	}()
	hello, err := client.Hello(ctx)
	if err != nil {
		return err
	}
	workdir := "/box/work"
	if len(hello.Repos) > 0 {
		workdir = filepath.Join(workdir, string(hello.Repos[0].Name))
	}
	if err := os.MkdirAll(workdir, 0755); err != nil {
		return err
	}
	setup := func(ctx context.Context) error {
		return setupRepos(ctx, "/box/work", "/box/state/setup", hello.Repos, hello.GitName, hello.GitEmail, runGit)
	}
	setupErr := setup(ctx)
	for _, link := range []struct{ path, target string }{
		{"/box/pi/AGENTS.md", "/run/dyna/instructions/global/AGENTS.md"},
		{"/box/work/AGENTS.md", "/run/dyna/instructions/room/AGENTS.md"},
	} {
		if err := os.MkdirAll(filepath.Dir(link.path), 0700); err != nil {
			return err
		}
		if err := os.Remove(link.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Symlink(link.target, link.path); err != nil {
			return err
		}
	}
	args, err := modelArgs(hello.Model, hello.CompactTokens)
	if err != nil {
		return err
	}
	sessions := filepath.Join(piDir, "sessions")
	if err = os.MkdirAll(sessions, 0700); err != nil {
		return err
	}
	if err := restoreTranscripts(ctx, sessions, hello.Transcripts, client.TranscriptFetch); err != nil {
		var rejected apiError
		if !errors.As(err, &rejected) || rejected.code != contract.ErrConflict {
			return err
		}
		log.Printf("transcript restore: %v", err)
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
	jobs := shell.New(shell.Config{Dir: "/box/work", After: time.After, Report: func(r contract.ShellJobEndedRequest) error { return client.ShellJobEnded(ctx, r) }})
	defer jobs.Close()
	client.shellJobs = jobs.Running
	mux := http.NewServeMux()
	mux.Handle("/shell/", http.StripPrefix("/shell", jobs.Handler()))
	mux.Handle("/", ModelRoute(engineSocket))
	mcpPath := filepath.Join(piDir, "mcp.json")
	dynaTools, problem := dynamcp.New(client.forward, dynamcp.BoxTools("/run/dyna/agent.sock"), post.Preparer{Dir: workdir, Render: visual.Preview, Upload: func(ctx context.Context, path string) (contract.Attachment, error) {
		return post.UploadHTTP(ctx, client.http, path)
	}})
	if problem != nil {
		return problem
	}
	mux.Handle("/dyna/", dynaTools)
	err = pi.WriteAgentMCPConfig(mcpPath, dynaTools.Description())
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux}
	defer server.Close()
	go func() { errs <- server.Serve(listener) }()
	extensionPath := filepath.Join(piDir, "dyna-call-id.ts")
	extension := strings.ReplaceAll(callIDExtension, "__DYNA_CALL_ID_ARGUMENT__", contract.ToolCallIDArgument)
	if err = os.WriteFile(extensionPath, []byte(extension), 0600); err != nil {
		return err
	}
	args = append(args, "--extension", extensionPath)
	args = append(args, "--append-system-prompt", hello.Instructions)
	args = append(args, "--mode", "rpc", "--models-file", modelsPath, "--session-dir", sessions, "--mcp-config", mcpPath, "--exclude-tools", "bash", "--continue")
	h, err := pi.Open(ctx, "pi", args, "", workdir)
	if err != nil {
		return err
	}
	defer h.Close()
	activity := &activity{}
	h.Activity = activity.set
	activityTicks := time.NewTicker(time.Second)
	defer activityTicks.Stop()
	go activity.run(ctx, activityTicks.C, client.TurnActivity)
	transcript := NewTranscript(client, sessions)
	transcript.syncLogged(ctx, h.SessionFile())
	ticks := time.NewTicker(transcriptInterval)
	defer ticks.Stop()
	go transcript.Run(ctx, ticks.C, h.SessionFile)
	runner := &Runner{Harness: h, Sink: endedSink{turnSink: client, transcript: transcript, file: h.SessionFile}, Setup: setup, setupErr: setupErr, Model: hello.Model}
	var closing atomic.Bool
	ownerInput := computer.NewInput(computer.URL)
	go ownerInput.Run(ctx)
	var stopWatch context.CancelFunc
	var stopShellWatch context.CancelFunc
	var shellDone chan struct{}
	stopShell := func() {
		if stopShellWatch != nil {
			stopShellWatch()
			<-shellDone
			stopShellWatch = nil
		}
	}
	go func() {
		errs <- client.Channel(ctx, func(message contract.BoxMessage) error {
			switch body := message.Body.(type) {
			case contract.ShellWatchStart:
				stopShell()
				var watch context.Context
				watch, stopShellWatch = context.WithCancel(ctx)
				shellDone = make(chan struct{})
				go func() {
					defer close(shellDone)
					client.watchShell(watch, jobs, body.Generation, time.NewTicker(250*time.Millisecond))
				}()
				return nil
			case contract.ShellWatchStop:
				stopShell()
				return nil
			case contract.ComputerWatchStart:
				if stopWatch != nil {
					stopWatch()
				}
				var watch context.Context
				watch, stopWatch = context.WithCancel(ctx)
				go client.watchComputer(watch, body.Generation)
				return nil
			case contract.ComputerInputBatch:
				// A worker sends input, so a stuck browser never holds control messages.
				if !ownerInput.Enqueue(body) {
					log.Print("computer input: queue full, batch dropped")
				}
				return nil
			case contract.ComputerWatchStop:
				if stopWatch != nil {
					stopWatch()
					stopWatch = nil
				}
				return nil
			}
			if _, ok := message.Body.(contract.Archive); ok {
				closing.Store(true)
				return closeBox(ctx, h, transcript, h.SessionFile(), time.After, func(ctx context.Context) error {
					return archiveAndReport(ctx, "/box/work", hello.Session, hello.Repos, runGit, time.After, client.ArchiveDone)
				})
			}
			return runner.Handle(ctx, message)
		}, func(connected bool) {
			if connected {
				select {
				case states <- struct{}{}:
				default:
				}
			}
			if !connected {
				stopShell()
			}
			if !connected && stopWatch != nil {
				stopWatch()
				stopWatch = nil
			}
		})
	}()
	return waitRun(ctx, errs, runner.Errors(), h.Done(), &closing)
}
