//go:build linux

package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/resident-labs/dyna/box"

	"github.com/resident-labs/dyna/box/computer"
	"github.com/resident-labs/dyna/box/dynaext"
	"github.com/resident-labs/dyna/box/dynamcp"
	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pig"
)

const (
	engineSocket = "/run/dyna/engine/engine.sock"
	piDir        = "/box/pi"
	shellURL     = "http://127.0.0.1:7781/shell/"
	// buildCacheSocket is the host's compiler cache. dyna-base's sccache reaches it at 127.0.0.1:7781/build-cache.
	buildCacheSocket = "/run/dyna/build-cache/cache.sock"
)

// boxHarness is what runs a box's turns: the session's Pi, or a crew.
type boxHarness interface {
	harness.Harness
	Done() <-chan struct{}
}

// crewInstructions is a crew worker's whole prompt: the core every agent shares, which box.hello sends as WorkerInstructions (never the session's own Instructions), and the role of a worker in a crew. The hello has a workflow.
func crewInstructions(hello contract.BoxHelloResponse, tree string) string {
	text := hello.WorkerInstructions + "\n\n# Your role: workflow worker\n\n" + "You are one worker of workflow " + hello.Workflow.Name + ", a process in your session's box, not a session of your own. Your final text is returned to the script as your result: it is a return value, not a message, so it holds the result itself and is not posted. With a schema, it is that JSON alone. Nothing wakes a worker: a command that outlasts bash's wait becomes a job, which job output reads until it ends, and a job waiting for the build lock is normal.\n"
	if tree == "" {
		return text + "Other workers share this box and this working tree: stay within the work your prompt gives you.\n"
	}
	return text + "You have your own git worktree at " + tree + " on your own branch, and other workers cannot see it. Work only there. When you end, whatever you left there is committed and pushed for you, except ignored files. Cargo builds share CARGO_TARGET_DIR with the other workers, so reach a binary with cargo run or through that folder, not through ./target.\n"
}

// workerConfig is what a crew worker opens with, on the box-wide config: its own tree, its profile's model and compaction trigger, its own models.json
// in folder, and a session file of its own, so that workers sharing the session folder never resume one another's.
func workerConfig(box pig.Config, profile contract.WorkflowProfile, folder, dir string) pig.Config {
	box.Dir, box.Model, box.CompactTokens, box.New = dir, profile.Model, profile.CompactTokens, true
	box.ModelInstructions = profile.ModelInstructions
	box.ModelsFile = filepath.Join(folder, "models.json")
	return box
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
	setup(ctx)
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
	addedServers, problems, err := roomAddons(hello.Root).apply(ctx)
	if err != nil {
		return err
	}
	// The session's Pi and every crew worker take the same servers. An owner's own server named github comes first.
	if _, taken := addedServers["github"]; hello.GitHub && !taken {
		addedServers["github"] = pig.GitHubServer()
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
	modelsPath := filepath.Join(piDir, "models.json")
	writeModels := func(textOnly []contract.TextOnlyModel) error {
		models, err := json.Marshal(Models(textOnly...))
		if err != nil {
			return err
		}
		return os.WriteFile(modelsPath, models, 0600)
	}
	if err = writeModels(hello.TextOnlyModels); err != nil {
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
	}}, hello.Discord)
	if problem != nil {
		return problem
	}
	mux.Handle("/dyna/", dynaTools)
	err = pig.WriteAgentMCPConfig(mcpPath, dynaTools.Description(), !hello.Root && hello.Workflow == nil, addedServers)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux}
	defer server.Close()
	go func() { errs <- server.Serve(listener) }()
	activity := &activity{}
	transcript := NewTranscript(client, sessions)
	var h boxHarness
	var crew *Crew                       // a crew's box runs a script, which a reload replaces
	var files func() []string            // the session files to keep sending
	var steered func(contract.Interrupt) // a crew's script takes no input, so its workers' commands never step aside
	if hello.Workflow != nil {
		open := func(ctx context.Context, profile contract.WorkflowProfile, folder, dir string, submit dynaext.Submit) (crewWorker, error) {
			tree := dir
			if dir == workdir {
				tree = ""
			}
			c := workerConfig(pig.Config{AgentDir: piDir, SessionDir: sessions, Notice: func(text string) { go notice(text) }}, profile, folder, dir)
			c.Instructions = crewInstructions(hello, tree)
			c.Extensions = []extension.InlineExtension{dynaext.Extension(dynaext.Config{Dir: dir, Shell: jobs, Dyna: dynaTools, Submit: submit})}
			return pig.Open(ctx, c)
		}
		config := CrewConfig{Workflow: *hello.Workflow, TextOnlyModels: hello.TextOnlyModels, Engine: client, Open: open, Transcript: transcript, Workdir: workdir, Sessions: sessions, State: piDir}
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
		interrupts := dynaext.NewInterrupts()
		session, err := pig.Open(ctx, pig.Config{
			Dir:               workdir,
			AgentDir:          piDir,
			SessionDir:        sessions,
			Model:             hello.Model,
			CompactTokens:     hello.CompactTokens,
			Instructions:      hello.Instructions,
			ModelInstructions: hello.ModelInstructions,
			Extensions:        []extension.InlineExtension{dynaext.Extension(dynaext.Config{Dir: workdir, Shell: jobs, Dyna: dynaTools, Interrupts: interrupts})},
			Notice:            func(text string) { go notice(text) },
		})
		if err != nil {
			return err
		}
		session.Activity = activity.set
		session.Finished = activity.finish
		h, files, steered = session, func() []string { return []string{session.SessionFile()} }, func(sender contract.Interrupt) { jobs.StepAside(sender); interrupts.Send(sender) }
	}
	defer h.Close()
	steps.Done("session start")
	activityTicks := time.NewTicker(time.Second)
	defer activityTicks.Stop()
	go activity.run(ctx, activityTicks.C, client.TurnActivity)
	transcript.syncLogged(ctx, files()...)
	ticks := time.NewTicker(transcriptInterval)
	defer ticks.Stop()
	go transcript.Run(ctx, ticks.C, files)
	runner := &Runner{Harness: h, Sink: endedSink{turnSink: client, transcript: transcript, files: files}, Setup: setup, Attach: client.Attach, Steered: steered}
	if crew == nil {
		// A crew's workers each read their own models.json, written when the worker opens.
		runner.Models = writeModels
	}
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
			if save, ok := message.Body.(contract.SaveDiff); ok {
				closing.Store(true)
				var report *contract.DiffSavedRequest // one try: a retry of the report sends this one again
				return closeBox(ctx, h, transcript, files, time.After, func(ctx context.Context) error {
					if report == nil {
						saved := saveDiff(ctx, "/box/work", diffFolder, hello.Session, save.Name, hello.Repos, runGit)
						report = &saved
					}
					return client.DiffSaved(ctx, *report)
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
