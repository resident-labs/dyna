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

	"github.com/MichaelKinsy/PiG/ai"
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
	"github.com/santhosh-tekuri/jsonschema/v6"
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

// crewInstructions follows the shared core with Claude Code's workflow worker instructions, adapted for Dyna.
// The step's schema goes in the prompt because providers don't support arbitrary JSON Schemas as tool parameters.
func crewInstructions(core string, schema json.RawMessage, tree, main string) string {
	text := core + "\n\n# Your role: workflow worker\n\nYou are a worker spawned by a workflow script. Use the tools available to complete the task.\n\n"
	if len(schema) > 0 {
		text += `CRITICAL: You MUST call StructuredOutput exactly once to return your final answer.
- Do your work, then call StructuredOutput with your answer in its result argument, matching the schema below.
- Do NOT put your answer in a text response or a post.
- If validation fails, read the tool error and call StructuredOutput again with a corrected result.
- A successful call ends the turn. No acknowledgment needed.

Result schema:
` + string(schema) + "\n"
	} else {
		text += `CRITICAL: Your final text response is returned **verbatim** as a string to the calling script — it is your return value, not a message to a human.
- Output the literal result (data, JSON, text). Do NOT output confirmations like "Done." or "Sent."
- If asked for JSON, return ONLY the raw JSON — no code fences, no prose, no markdown.
- Do NOT use post to deliver your answer. Put your answer in your final text response.
- Be concise. The script will parse your output.
`
	}
	const jobs = " Nothing wakes a worker, and ending your turn ends the step. So wait for a job inside the turn with job output, despite the rule above."
	if tree == "" {
		return text + "\n---\nOther workers share this box and this working tree: stay within the work your prompt gives you." + jobs + "\n"
	}
	return text + "\n---\nYou are running in an isolated git worktree at `" + tree + "` (a separate working copy of the repo). Changes you make here do NOT affect the main working directory (`" + main + "`) or other workers. Work normally — what you leave here, except ignored files, is committed and pushed for you when you end. Cargo builds share CARGO_TARGET_DIR with the other workers, so reach a binary with cargo run or through that folder, not through ./target." + jobs + "\n"
}

// Run connects the box, serves its model route, and runs the Pi session.
func Run(ctx context.Context) error {
	if err := ai.ConfigureHTTPDispatcher(int(contract.ModelWireIdleTimeout.Milliseconds())); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	steps := box.NewStepLog("box agent")
	client := NewClient(engineSocket)
	errs := make(chan error, 4)
	states := make(chan struct{}, 1)
	var computerRunning atomic.Bool
	desktop, closeComputer, err := computer.Serve(ctx, "/run/dyna/agent.sock", errs, func(running bool) {
		computerRunning.Store(running)
		select {
		case states <- struct{}{}:
		default:
		}
	}, client)
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
		return setupRepos(ctx, "/box/work", "/box/state/setup", hello.Repos, hello.GitName, hello.GitEmail, hello.GitSigningKey, runGit)
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
	mux.Handle(agyDynaPath, dynaTools.Agy())
	err = pig.WriteAgentMCPConfig(mcpPath, dynaTools.Description(), !hello.Root && hello.Workflow == nil, hello.Advisor, addedServers)
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
		open := func(ctx context.Context, profile contract.WorkflowProfile, folder, dir string, document json.RawMessage, schema *jsonschema.Schema) (crewWorker, error) {
			tree := dir
			if dir == workdir {
				tree = ""
			}
			c := workerConfig(pig.Config{AgentDir: piDir, SessionDir: sessions, Notice: func(text string) { go notice(text) }}, profile, folder, dir)
			c.Instructions, c.NoSkills = crewInstructions(hello.WorkerInstructions, document, tree, workdir), true
			if schema != nil {
				c.ResultTool = structuredOutput(schema)
			}
			c.Extensions = []extension.InlineExtension{dynaext.Extension(dynaext.Config{Dir: dir, Shell: jobs, Dyna: dynaTools})}
			return pig.Open(ctx, c)
		}
		config := CrewConfig{Workflow: *hello.Workflow, Engine: client, Open: open, Transcript: transcript, Workdir: workdir, Sessions: sessions, State: piDir}
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
		if hello.Harness == contract.HarnessAgy {
			session, err := openAgy(ctx, hello, workdir, sessions, agyContextFiles, activity, func(text string) { go notice(text) })
			if err != nil {
				return err
			}
			h, files = session, func() []string { return []string{session.SessionFile()} }
		} else {
			pigNew := hello.Fresh
			var history string
			due := piHandoffDue(hello, handoffMarker)
			if due {
				if history, err = handoffHistory(sessions); err != nil {
					return err
				}
				pigNew = true
			}
			interrupts := dynaext.NewInterrupts()
			session, err := pig.Open(ctx, pig.Config{
				Dir:               workdir,
				AgentDir:          piDir,
				SessionDir:        sessions,
				Model:             hello.Model,
				CompactTokens:     hello.CompactTokens,
				Instructions:      hello.Instructions,
				ModelInstructions: hello.ModelInstructions,
				New:               pigNew,
				Extensions:        []extension.InlineExtension{dynaext.Extension(dynaext.Config{Dir: workdir, Shell: jobs, Dyna: dynaTools, Interrupts: interrupts})},
				Notice:            func(text string) { go notice(text) },
			})
			if err != nil {
				return err
			}
			session.Activity = activity.set
			session.Finished = activity.finish
			var piHarness boxHarness = session
			if due {
				piHarness = &handoffStart{boxHarness: session, history: history, gen: hello.HandoffGen, marker: handoffMarker}
			}
			h, files, steered = piHarness, func() []string { return []string{session.SessionFile()} }, func(sender contract.Interrupt) { jobs.StepAside(sender); interrupts.Send(sender) }
		}
	}
	defer h.Close()
	// In-box agents run beside the session's own turn, in this process. A crew's box runs none yet.
	var agents *Agents
	if crew == nil {
		sessionFiles := files
		agents = NewAgents(ctx, AgentsConfig{
			Engine:     client,
			Transcript: transcript,
			Sessions:   sessions,
			Orient: func(ctx context.Context) string {
				return "Your parent's conversation is the file " + sessionFiles()[0] + ": it holds everything its agent has done and been asked, and you can read it.\n\nThe state of its working repository " + workdir + ":\n" + gitBlock(ctx, workdir)
			},
			Open: func(ctx context.Context, turn contract.AgentTurn, dir string, toolCall func()) (agentSession, error) {
				return openAgent(ctx, piDir, workdir, hello.WorkerInstructions, Models(hello.TextOnlyModels...), func(text string) { go notice(text) }, turn, dir, toolCall)
			},
		})
		files = func() []string { return append(sessionFiles(), agents.Files()...) }
	}
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
			case contract.AgentTurn:
				if agents == nil {
					go client.AgentEnded(ctx, contract.AgentEndedRequest{ID: body.ID, Outcome: contract.TurnFailed, Error: "this box runs no agents"})
					return nil
				}
				agents.Turn(body)
				return nil
			case contract.AgentStop:
				if agents != nil {
					agents.Stop(body.ID)
				}
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
					if err := desktop.Stop(); err != nil {
						log.Printf("closeBox: stop computer: %v", err)
					}
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
