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

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/computer"
	"github.com/resident-labs/dyna/box/dynamcp"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pi"
)

const (
	engineSocket = "/run/dyna/engine/engine.sock"
	piDir        = "/box/pi"
	shellURL     = "http://127.0.0.1:7781/shell/"
	// buildCacheSocket is the host's compiler cache. dyna-base's sccache reaches it at 127.0.0.1:7781/build-cache.
	buildCacheSocket = "/run/dyna/build-cache/cache.sock"
)

//go:embed extension.js
var extensionSource string

// RenderExtension fills in the values the extension shares with the box agent.
func RenderExtension() string {
	return strings.NewReplacer("__DYNA_CALL_ID_ARGUMENT__", contract.ToolCallIDArgument, "__DYNA_MCP_URL__", pi.DynaMCPURL, "__DYNA_SHELL_URL__", shellURL, "__DYNA_ATTACHMENTS__", box.AttachmentFolder).Replace(extensionSource)
}

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

// piArgs appends the fixed Pi flags to the model flags. "+codemode" adds the
// codemode tool to Pi's default tools, so it stays active whatever MCP
// exposure the servers use. The extension's bash takes the place of Pi's, and
// "+bash" keeps it active in a resumed session: Pi restores the tools a
// transcript recorded, and applies these on top.
func piArgs(args []string, extensionPath, instructions, modelsPath, sessions, mcpPath string) []string {
	args = append(args, "--extension", extensionPath, "--append-system-prompt", instructions)
	return append(args, "--mode", "rpc", "--models-file", modelsPath, "--session-dir", sessions, "--mcp-config", mcpPath, "--tools", "+codemode,+bash")
}

// boxHarness is what runs a box's turns: the session's Pi, or a crew.
type boxHarness interface {
	harness.Harness
	Done() <-chan struct{}
}

// crewInstructions follow a session's own instructions in each worker.
func crewInstructions(name, tree string) string {
	text := "You are one worker of workflow " + name + ", a process in your session's box, not a session of your own. Your final text is returned to the script as your result: it is a return value, not a message, so it holds the result itself and is not posted. With a schema, it is that JSON alone. Nothing wakes a worker: a command that outlasts bash's wait becomes a job, which job output reads until it ends, and a job waiting for the build lock is normal.\n"
	if tree == "" {
		return text + "Other workers share this box and this working tree: stay within the work your prompt gives you.\n"
	}
	return text + "You have your own git worktree at " + tree + " on your own branch, and other workers cannot see it. Work only there. When you end, whatever you left there is committed and pushed for you, except ignored files. Cargo builds share CARGO_TARGET_DIR with the other workers, so reach a binary with cargo run or through that folder, not through ./target.\n"
}

// Run connects the box, serves its model route, and runs the Pi session.
func Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	steps := box.NewStepLog("box agent")
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
	steps.Done("hello")
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
	steps.Done("repository setup")
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
	addedServers, problems, err := roomAddons().apply(ctx)
	if err != nil {
		return err
	}
	// The session's Pi and every crew worker take the same servers. An owner's own server named github comes first.
	if _, taken := addedServers["github"]; hello.GitHub && !taken {
		addedServers["github"] = pi.GitHubServer()
	}
	steps.Done("add-ons")
	notice := func(text string) {
		if err := client.Notice(ctx, text); err != nil {
			log.Printf("notice: %v", err)
		}
	}
	if text := addonNotice(problems); text != "" {
		notice(text)
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
	models, err := json.Marshal(Models(hello.Profiles...))
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
	jobs := shell.New(shell.Config{After: time.After, Report: func(r contract.ShellJobEndedRequest) error { return client.ShellJobEnded(ctx, r) }, Running: func(r contract.ShellJobRunningRequest) error { return client.ShellJobRunning(ctx, r) }, Host: client.HostRun, HostStop: client.HostStop})
	defer jobs.Close()
	client.shellJobs = jobs.Running
	mux := http.NewServeMux()
	mux.Handle("/shell/", http.StripPrefix("/shell", jobs.Handler()))
	mux.Handle("/build-cache/", http.StripPrefix("/build-cache", BuildCacheRoute(buildCacheSocket)))
	mux.Handle("/", ModelRoute(engineSocket))
	mcpPath := filepath.Join(piDir, "mcp.json")
	dynaTools, problem := dynamcp.New(client.forward, dynamcp.BoxTools("/run/dyna/agent.sock"), post.Preparer{Room: hello.Room, Dir: workdir, Render: visual.Preview, Upload: func(ctx context.Context, path string) (contract.Attachment, error) {
		return post.UploadHTTP(ctx, client.http, "", path)
	}})
	if problem != nil {
		return problem
	}
	mux.Handle("/dyna/", dynaTools)
	err = pi.WriteAgentMCPConfig(mcpPath, dynaTools.Description(), addedServers)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux}
	defer server.Close()
	go func() { errs <- server.Serve(listener) }()
	extensionPath := filepath.Join(piDir, "dyna.js")
	if err = os.WriteFile(extensionPath, []byte(RenderExtension()), 0600); err != nil {
		return err
	}
	activity := &activity{}
	transcript := NewTranscript(client, sessions)
	var h boxHarness
	var crew *Crew            // a crew's box runs a script, which a reload replaces
	var files func() []string // the session files to keep sending
	var steered func()        // a crew's script takes no input, so its workers' commands never step aside
	if hello.Workflow != nil {
		open := func(ctx context.Context, profile contract.WorkflowProfile, folder, dir string) (crewWorker, error) {
			args, err := modelArgs(profile.Model, profile.CompactTokens)
			if err != nil {
				return nil, err
			}
			mcp := filepath.Join(folder, "mcp.json")
			if err := pi.WriteAgentMCPConfig(mcp, dynaTools.Description(), addedServers); err != nil {
				return nil, err
			}
			tree := dir
			if dir == workdir {
				tree = ""
			}
			worker, err := pi.Open(ctx, "pi", piArgs(args, extensionPath, hello.Instructions+crewInstructions(hello.Workflow.Name, tree), filepath.Join(folder, "models.json"), sessions, mcp), "", dir)
			if err != nil {
				return nil, err
			}
			return worker, nil
		}
		config := CrewConfig{Workflow: *hello.Workflow, Profiles: hello.Profiles, Engine: client, Open: open, Transcript: transcript, Workdir: workdir, Sessions: sessions, State: piDir}
		if len(hello.Repos) > 0 {
			config.Branch, config.Base = hello.Repos[0].Branch, hello.Repos[0].Commit
		}
		crew, err = NewCrew(config)
		if err != nil {
			return err
		}
		if err := crew.Export(); err != nil {
			return err
		}
		h, files = crew, crew.Files
	} else {
		session, err := pi.Open(ctx, "pi", append(piArgs(args, extensionPath, hello.Instructions, modelsPath, sessions, mcpPath), "--continue"), "", workdir, func(text string) { go notice(text) })
		if err != nil {
			return err
		}
		session.Activity = activity.set
		h, files, steered = session, func() []string { return []string{session.SessionFile()} }, jobs.StepAside
	}
	defer h.Close()
	steps.Done("pi start")
	activityTicks := time.NewTicker(time.Second)
	defer activityTicks.Stop()
	go activity.run(ctx, activityTicks.C, client.TurnActivity)
	transcript.syncLogged(ctx, files()...)
	ticks := time.NewTicker(transcriptInterval)
	defer ticks.Stop()
	go transcript.Run(ctx, ticks.C, files)
	runner := &Runner{Harness: h, Sink: endedSink{turnSink: client, transcript: transcript, files: files}, Setup: setup, Attach: client.Attach, Steered: steered, setupErr: setupErr}
	var closing atomic.Bool
	var stopShellWatch context.CancelFunc
	var shellDone chan struct{}
	stats := newBoxStats()
	stopShell := func() {
		if stopShellWatch != nil {
			stopShellWatch()
			<-shellDone
			stopShellWatch = nil
		}
	}
	binary, err := runningSum()
	if err != nil {
		log.Printf("binary checksum: %v", err)
	}
	go func() {
		errs <- client.Channel(ctx, binary, func(message contract.BoxMessage) error {
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
			case contract.BoxStatsAsk:
				// A first reading takes a moment, and the channel keeps reading meanwhile.
				go client.reportStats(ctx, stats, body.Ask)
				return nil
			case contract.WorkflowReload:
				if crew != nil {
					crew.Reload(body.Workflow)
				}
				return nil
			}
			if _, ok := message.Body.(contract.Archive); ok {
				closing.Store(true)
				return closeBox(ctx, h, transcript, files, time.After, func(ctx context.Context) error {
					return archiveAndReport(ctx, "/box/work", hello.Session, hello.Repos, runGit, archiveLimit, time.After, client.ArchiveDone)
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
		})
	}()
	return waitRun(ctx, errs, runner.Errors(), h.Done(), &closing)
}
