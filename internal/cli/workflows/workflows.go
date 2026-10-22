package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/parentoutput"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runsvc"
	"dyna-agent/internal/workflow"
	"dyna-agent/internal/workflowsvc"
	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktreesvc"
)

func loadStore() (*profile.Store, error) {
	return profile.Load(profile.DefaultPath())
}

// applyRunRoom resolves this run's room and stamps it into the process
// environment for runstore.Create, the engine, and detached children.
func applyRunRoom(ref string) (room.Record, error) {
	rec, err := room.DefaultStore().ResolveContext(ref, false)
	if err != nil {
		return room.Record{}, err
	}
	_ = os.Setenv(mode.EnvRoomID, rec.ID)
	_ = os.Setenv(mode.EnvRoom, rec.Name)
	return rec, nil
}

var (
	stDim   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "244", Dark: "243"})
	stOK    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "35", Dark: "42"})
	stErr   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
	stWarn  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "214"})
	stPhase = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "63", Dark: "111"})
	stName  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "31", Dark: "81"})
)

func NewRunCommand() *cobra.Command {
	var argsJSON, name, roomRef, dir, resumeID, workflowDir string
	var quiet, asJSON, detach, planOnly bool
	var maxConc, maxAgents int
	cmd := &cobra.Command{
		Use:   "run <script.js>",
		Short: "Execute a workflow script (progress on stderr, result JSON on stdout)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if !planOnly {
				if err := authority.Check("run", authority.Current(), nil); err != nil {
					return err
				}
			}
			if _, err := applyRunRoom(roomRef); err != nil {
				return err
			}
			src, err := os.ReadFile(a[0])
			if err != nil {
				return err
			}
			var argsVal any
			if argsJSON != "" {
				if err := json.Unmarshal([]byte(argsJSON), &argsVal); err != nil {
					return fmt.Errorf("--args must be valid JSON: %w", err)
				}
			}
			if dir == "" {
				dir, _ = os.Getwd()
			}
			scriptPath := a[0]
			if original := os.Getenv("DYNA_SCRIPT_PATH"); original != "" {
				scriptPath = original
			}
			scriptPath, _ = filepath.Abs(scriptPath)
			if planOnly {
				p := plan.Extract(string(src), plan.Options{
					SourcePath: scriptPath, WorkDir: dir, WorkflowDir: workflowDir,
					ResumedFrom: resumeID,
				})
				b, err := json.MarshalIndent(p, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(c.OutOrStdout(), string(b))
				return nil
			}
			opts := workflowsvc.Options{ScriptSrc: string(src), ScriptPath: scriptPath, Args: argsVal, Name: name, WorkDir: dir, WorkflowDir: workflowDir, ResumeFrom: resumeID, MaxConc: maxConc, MaxAgents: maxAgents}
			if detach {
				id, err := workflowsvc.Launch(c.Context(), opts, workflowsvc.LaunchOptions{OnWarning: runObserver(c, quiet, resumeID).OnWarning})
				if err != nil {
					return err
				}
				fmt.Fprintln(c.OutOrStdout(), id)
				fmt.Fprintln(c.ErrOrStderr(), stDim.Render("running in background; completion will be messaged to its parent or room, watch live in the desktop app or with `dyna runs show`"))
				return nil
			}
			output := "plain"
			if asJSON {
				output = "json"
			}
			return runForeground(c, opts, quiet, output)
		},
	}
	cmd.Flags().StringVar(&argsJSON, "args", "", "JSON value exposed to the script as `args`")
	cmd.Flags().StringVar(&name, "name", "", "run name (default: meta.name from the script)")
	cmd.Flags().StringVar(&roomRef, "room", "", "room name or ID (default: inherited room, else the current repo's room)")
	cmd.Flags().StringVar(&dir, "dir", "", "working directory for workers (default: cwd)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress progress on stderr")
	cmd.Flags().BoolVar(&asJSON, "json", false, "wrap stdout result with runId/status envelope")
	cmd.Flags().IntVar(&maxConc, "max-concurrent", 0, "max concurrent workers (default min(16, cores-2))")
	cmd.Flags().IntVar(&maxAgents, "max-agents", 0, "lifetime agent() cap for this run (default 1000)")
	cmd.Flags().StringVar(&resumeID, "resume", "", "run id to resume: unchanged agent() calls replay from its journal")
	cmd.Flags().StringVar(&workflowDir, "workflows-dir", "", "name-keyed workflow registry (default user config, then ./examples)")
	cmd.Flags().BoolVar(&detach, "detach", false, "run in the background; prints the run id and messages completion to its parent or room")
	cmd.Flags().BoolVar(&planOnly, "plan", false, "extract and print the versioned planned graph; start no workers")
	return cmd
}

func resumeCacheReport(stats workflow.CacheStats) (string, bool) {
	message := fmt.Sprintf("resume cache: %d/%d prior journaled calls replayed", stats.Hits, stats.PriorCalls)
	suspicious := stats.PriorCalls > 0 && stats.Hits*2 < stats.PriorCalls
	if suspicious {
		message += "; warning: suspiciously low hit rate may indicate prompt/schema nondeterminism or workflow changes"
	}
	return message, suspicious
}

func runObserver(c *cobra.Command, quiet bool, resumeID string) workflowsvc.Observer {
	observer := workflowsvc.Observer{
		OnWarning: func(message string) { fmt.Fprintln(c.ErrOrStderr(), stWarn.Render("warning: "+message)) },
		OnStart: func(meta runstore.Meta) {
			fmt.Fprintln(c.ErrOrStderr(), stDim.Render("run "+meta.ID+"  •  watch live in the desktop app or with `dyna runs show "+meta.ID+"`"))
		},
		OnResume: func(count int) {
			fmt.Fprintln(c.ErrOrStderr(), stDim.Render(fmt.Sprintf("resuming from %s (%d journaled calls)", resumeID, count)))
		},
		OnCache: func(stats workflow.CacheStats) {
			message, suspicious := resumeCacheReport(stats)
			style := stDim
			if suspicious {
				style = stWarn
			}
			fmt.Fprintln(c.ErrOrStderr(), style.Render(message))
		},
	}
	if !quiet {
		observer.OnEvent = func(e runstore.Event) { printEvent(c.ErrOrStderr(), e) }
	}
	return observer
}

// NewRunWorkerCommand is the private execution entry point for registered runs.
func NewRunWorkerCommand() *cobra.Command {
	var output string
	var quiet bool
	cmd := &cobra.Command{Use: "run-worker <options.json>", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		switch output {
		case "raw", "plain", "json":
		default:
			return fmt.Errorf("unknown workflow output mode %q", output)
		}
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		var opts workflowsvc.Options
		if err := json.Unmarshal(data, &opts); err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(c.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		result, err := workflowsvc.Execute(ctx, opts, nil, runObserver(c, quiet, opts.ResumeFrom))
		if err != nil {
			return err
		}
		return writeRunResult(c, result, output)
	}}
	cmd.Flags().StringVar(&output, "output", "raw", "private result presentation: raw, plain, or json")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress progress events")
	return cmd
}

func writeRunResult(c *cobra.Command, result workflowsvc.Result, output string) error {
	if output != "raw" {
		var err error
		result.Value, err = parentoutput.JSON(runstore.RunDir(result.ID), result.Value)
		if err != nil {
			return fmt.Errorf("prepare parent result: %w", err)
		}
	}
	if output == "json" {
		out, _ := json.Marshal(map[string]any{"runId": result.ID, "status": result.Status, "result": json.RawMessage(orNull(result.Value))})
		fmt.Fprintln(c.OutOrStdout(), string(out))
	} else {
		fmt.Fprintln(c.OutOrStdout(), result.Value)
	}
	return nil
}

func printEvent(w io.Writer, e runstore.Event) {
	indent := ""
	if e.Workflow != "" {
		indent = "    "
	}
	switch e.T {
	case "phase":
		fmt.Fprintln(w, stPhase.Render(indent+"── "+e.Title+" ──"))
	case "workflow_start":
		fmt.Fprintln(w, stPhase.Render("  ◇ workflow ")+stName.Render(e.Title)+stDim.Render("  "+e.Workflow))
	case "agent_start":
		fmt.Fprintln(w, stDim.Render(indent+"  ◌ queued  ")+stName.Render(e.Label)+stDim.Render("  ["+e.Profile+"]  "+e.Preview))
	case "agent_run":
		fmt.Fprintln(w, stDim.Render(indent+"  ▶ running ")+stName.Render(e.Label)+stDim.Render("  ["+e.Profile+"]"))
	case "agent_steer":
		fmt.Fprintln(w, stPhase.Render(indent+"  ↪ steered ")+stName.Render(e.Label)+stDim.Render("  "+e.Preview))
	case "agent_end":
		d := time.Duration(e.DurMs) * time.Millisecond
		switch {
		case e.Cached:
			fmt.Fprintln(w, stOK.Render(indent+"  ⚡ cached  ")+stName.Render(e.Label)+stDim.Render("  "+e.Preview))
		case e.Status == "ok":
			fmt.Fprintln(w, stOK.Render(indent+"  ✓ done    ")+stName.Render(e.Label)+stDim.Render(fmt.Sprintf("  %s  %s", d.Round(time.Second), e.Preview)))
		default:
			fmt.Fprintln(w, stErr.Render(indent+"  ✗ failed  ")+stName.Render(e.Label)+stErr.Render("  "+e.Error))
		}
	case "workflow_end":
		if e.Status == "ok" {
			fmt.Fprintln(w, stOK.Render("  ◆ workflow done  ")+stName.Render(e.Title)+stDim.Render("  "+(time.Duration(e.DurMs)*time.Millisecond).Round(time.Second).String()))
		} else {
			fmt.Fprintln(w, stErr.Render("  ◇ workflow failed  ")+stName.Render(e.Title)+stErr.Render("  "+e.Error))
		}
	case "log":
		fmt.Fprintln(w, indent+"  › "+e.Msg)
	case "group_start":
		fmt.Fprintln(w, stDim.Render(indent+"  ▸ "+e.Title))
	case "group_end":
		fmt.Fprintln(w, stDim.Render(indent+"  ▸ end "+e.Title))
	}
}

// ---------- runs ----------

func NewRunsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Inspect past and active workflow runs",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	var asJSON, listArchived, listAll bool
	var session string
	list := &cobra.Command{
		Use:   "list",
		Short: "List live runs, newest first (--archived / --all include archived runs)",
		RunE: func(c *cobra.Command, _ []string) error {
			if listArchived && listAll {
				return fmt.Errorf("--archived and --all are mutually exclusive")
			}
			runs, err := listRuns(listArchived, listAll)
			if err != nil {
				return err
			}
			if session != "" {
				runs = slices.DeleteFunc(runs, func(run listedRun) bool { return run.Session != session })
			}
			if asJSON {
				b, _ := json.MarshalIndent(runs, "", "  ")
				fmt.Fprintln(c.OutOrStdout(), string(b))
				return nil
			}
			for _, r := range runs {
				status := r.Status
				if status == "running" && runstore.IsPaused(r.ID) {
					status = "paused"
				}
				name := r.Name
				if r.Archived && listAll {
					name += "  [archived]"
				}
				fmt.Fprintf(c.OutOrStdout(), "%-32s %-9s %-20s %s\n", r.ID, status, r.StartedAt.Format("2006-01-02 15:04:05"), name)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	list.Flags().StringVar(&session, "session", "", "only runs started from this session (DYNA_SESSION)")
	list.Flags().BoolVar(&listArchived, "archived", false, "list only archived runs")
	list.Flags().BoolVar(&listAll, "all", false, "list live and archived runs")

	var showJSON bool
	show := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Show a run's events and result",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			events, err := runstore.ReadEvents(a[0])
			if err != nil {
				return err
			}
			result, _ := runstore.ReadResult(a[0])
			if showJSON {
				dir := runstore.RunDir(a[0])
				p, _ := plan.ReadFile(dir)
				meta, _ := runstore.ReadMeta(a[0])
				terminal := meta.Status != "" && meta.Status != "running"
				rec := plan.Reconcile(p, eventViews(events), terminal)
				out := map[string]any{
					"events": events, "result": json.RawMessage(orNull(result)),
					"plan": p, "reconciliation": rec,
					"mermaid": map[string]string{
						"combined": plan.RenderMermaid(p, rec, plan.MermaidCombined),
						"planned":  plan.RenderMermaid(p, rec, plan.MermaidPlanned),
						"actual":   plan.RenderMermaid(p, rec, plan.MermaidActual),
					},
				}
				b, _ := json.MarshalIndent(out, "", "  ")
				fmt.Fprintln(c.OutOrStdout(), string(b))
				return nil
			}
			for _, e := range events {
				printEvent(c.ErrOrStderr(), e)
			}
			if result != "" {
				fmt.Fprintln(c.OutOrStdout(), result)
			}
			return nil
		},
	}
	show.Flags().BoolVar(&showJSON, "json", false, "machine-readable output")

	cancel := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Stop a running workflow (in-flight workers are killed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if err := engineclient.New("").Call(c.Context(), "CancelRun", []any{a[0]}, nil); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "canceled "+a[0])
			return nil
		},
	}

	pause := &cobra.Command{
		Use:   "pause <run-id>",
		Short: "Pause a running workflow: running workers finish, no new ones start",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if err := engineclient.New("").Call(c.Context(), "PauseRun", []any{a[0], true}, nil); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "paused "+a[0]+"; resume with `dyna runs unpause "+a[0]+"`")
			return nil
		},
	}

	unpause := &cobra.Command{
		Use:   "unpause <run-id>",
		Short: "Resume a paused workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			if err := engineclient.New("").Call(c.Context(), "PauseRun", []any{a[0], false}, nil); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "resumed "+a[0])
			return nil
		},
	}

	var removeWorktreeDisposition string
	remove := &cobra.Command{
		Use:     "remove <run-id>...",
		Aliases: []string{"rm", "delete"},
		Short:   "Delete finished runs, live or archived (cancel running ones first)",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			disposition, err := worktrees.ParseDisposition(removeWorktreeDisposition)
			if err != nil {
				return err
			}
			for _, id := range a {
				var result worktreesvc.DispositionResult
				if err := engineclient.New("").Call(c.Context(), "RemoveRun", []any{id, disposition}, &result); err != nil {
					return err
				}
				if err := printDisposition(c, result); err != nil {
					return err
				}
				fmt.Fprintln(c.OutOrStdout(), "removed "+id)
			}
			return nil
		},
	}
	remove.Flags().StringVar(&removeWorktreeDisposition, "worktrees", string(worktrees.DispositionRetain), "retained worktrees: retain, remove-unchanged, or remove-all")

	var clearWorktreeDisposition string
	clear := &cobra.Command{
		Use:   "clear",
		Short: "Delete every finished live run (archived runs are kept)",
		RunE: func(c *cobra.Command, _ []string) error {
			disposition, err := worktrees.ParseDisposition(clearWorktreeDisposition)
			if err != nil {
				return err
			}
			var cleared runsvc.Cleared
			if err := engineclient.New("").Call(c.Context(), "ClearRuns", []any{disposition}, &cleared); err != nil {
				return err
			}
			for _, result := range cleared.Worktrees {
				_ = printDisposition(c, result)
			}
			var failures []error
			for _, failure := range cleared.Failures {
				fmt.Fprintln(c.ErrOrStderr(), "warning: "+failure)
				failures = append(failures, errors.New(failure))
			}
			fmt.Fprintf(c.OutOrStdout(), "removed %d run(s)\n", cleared.Removed)
			return errors.Join(failures...)
		},
	}
	clear.Flags().StringVar(&clearWorktreeDisposition, "worktrees", string(worktrees.DispositionRetain), "retained worktrees: retain, remove-unchanged, or remove-all")

	var graphView string
	graph := &cobra.Command{
		Use:   "graph <run-id>",
		Short: "Print a deterministic Mermaid graph of a run's planned and actual topology",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, a []string) error {
			events, err := runstore.ReadEvents(a[0])
			if err != nil {
				return err
			}
			p, _ := plan.ReadFile(runstore.RunDir(a[0]))
			meta, _ := runstore.ReadMeta(a[0])
			terminal := meta.Status != "" && meta.Status != "running"
			rec := plan.Reconcile(p, eventViews(events), terminal)
			view := plan.MermaidView(graphView)
			switch view {
			case plan.MermaidPlanned, plan.MermaidActual, plan.MermaidCombined:
			default:
				view = plan.MermaidCombined
			}
			fmt.Fprint(c.OutOrStdout(), plan.RenderMermaid(p, rec, view))
			return nil
		},
	}
	graph.Flags().StringVar(&graphView, "view", "combined", "planned, actual, or combined")

	cmd.AddCommand(list, show, graph, newOutputCommand(), cancel, pause, unpause, remove, clear,
		newArchiveCommand(), newUnarchiveCommand())
	return cmd
}

func printWorktreeActions(w io.Writer, actions []worktrees.Action) {
	for _, action := range actions {
		switch action.Action {
		case worktrees.ActionRemoved:
			fmt.Fprintln(w, "removed worktree "+action.Path)
		case worktrees.ActionSkipped:
			if action.State == worktrees.StateError {
				fmt.Fprintln(w, "skipped uninspectable worktree "+action.Path)
			} else {
				fmt.Fprintln(w, "skipped modified worktree "+action.Path)
			}
		default:
			fmt.Fprintln(w, "retained worktree "+action.Path+"; manage with `dyna git worktrees list`")
		}
	}
}

// printDisposition prints a run's worktree warnings and actions, and returns
// its failure.
func printDisposition(c *cobra.Command, result worktreesvc.DispositionResult) error {
	for _, warning := range result.Warnings {
		fmt.Fprintln(c.ErrOrStderr(), "warning: "+warning)
	}
	if err := result.Err(); err != nil {
		return err
	}
	printWorktreeActions(c.OutOrStdout(), result.Actions)
	return nil
}

func eventViews(events []runstore.Event) []plan.EventView {
	out := make([]plan.EventView, len(events))
	for i, e := range events {
		out[i] = plan.EventView{
			T: e.T, ID: e.ID, Label: e.Label, Profile: e.Profile, Phase: e.Phase,
			Status: e.Status, Cached: e.Cached, Workflow: e.Workflow,
			PlanNodeID: e.PlanNodeID, WorkflowPlanID: e.WorkflowPlanID,
		}
	}
	return out
}

func NewDemoCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "demo",
		Short: "Register mock worker profiles and run a sample workflow",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := authority.Check("demo", authority.Current(), nil); err != nil {
				return err
			}
			s, err := loadStore()
			if err != nil {
				return err
			}
			demos := []profile.Profile{
				{Name: "mock-workhorse", Harness: profile.HarnessMock, Taste: 4, Intelligence: 10, Cost: 6, Default: true,
					Description: "Demo worker. Pretend GPT-class workhorse: grinds long tasks alone, correct but unpolished code, weak frontend taste."},
				{Name: "mock-reviewer", Harness: profile.HarnessMock, Taste: 10, Intelligence: 8, Cost: 4,
					Description: "Demo worker. Pretend Opus-class reviewer: excellent taste, finds subtle issues, great frontend instincts, pricey."},
				{Name: "mock-sprinter", Harness: profile.HarnessMock, Taste: 8, Intelligence: 6, Cost: 10,
					Description: "Demo worker. Pretend GLM-class sprinter: fast and very cheap, great taste, slightly below the big models."},
			}
			for _, d := range demos {
				if _, exists := s.Get(d.Name); !exists {
					if err := s.Upsert(d); err != nil {
						return err
					}
				}
			}
			if err := s.Save(); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "registered demo profiles: mock-workhorse, mock-reviewer, mock-sprinter")
			fmt.Fprintln(c.OutOrStdout(), "running demo workflow...")

			if _, err := applyRunRoom(""); err != nil {
				return err
			}
			cwd, _ := os.Getwd()
			if err := runForeground(c, workflowsvc.Options{Name: "demo-fanout", ScriptSrc: demoScript, WorkDir: cwd}, false, "raw"); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), stDim.Render("open the desktop app to browse this run"))
			return nil
		},
	}
}

const demoScript = `export const meta = { name: 'demo-fanout', description: 'demo', phases: [{title:'Scan'},{title:'Verify'}] }
phase('Scan')
log('fanning out 3 scanners')
const found = await parallel(['auth', 'billing', 'frontend'].map(area => () =>
  agent('Scan the ' + area + ' area for issues. RESPOND: {"area":"' + area + '","issues":' + (area === 'billing' ? 2 : 1) + '}',
    { profile: 'mock-sprinter', label: 'scan:' + area, schema: { type:'object', required:['area','issues'] } })))
const total = found.filter(Boolean).reduce((n, f) => n + f.issues, 0)
log(total + ' issues found, verifying')
const verdicts = await pipeline(found.filter(Boolean),
  (f) => agent('Adversarially verify issues in ' + f.area + '. RESPOND: verified ' + f.area,
    { profile: 'mock-reviewer', label: 'verify:' + f.area, phase: 'Verify' }))
return { totalIssues: total, verdicts }
`

// ---------- helpers ----------

func orNull(s string) string {
	if strings.TrimSpace(s) == "" {
		return "null"
	}
	return s
}
