// Package subagent is `dyna session`: long-lived, steerable, resumable worker
// sessions. `dyna subagent` remains an alias.
package subagent

import (
	"context"
	"dyna-agent/internal/transcript"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/exitcode"
	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
)

// NewCommand builds `dyna session` over the default store.
func NewCommand() *cobra.Command {
	return newCommand(func() *subagent.Manager { return notify.Default().Sessions })
}

func newCommand(newManager func() *subagent.Manager) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "session",
		Aliases: []string{"subagent"},
		Short:   "Long-lived, steerable worker sessions (subagent mode)",
		Args:    cobra.NoArgs,
		RunE:    func(c *cobra.Command, _ []string) error { return c.Help() },
		Long: `Manage sessions with ` + "`dyna session`" + `. ` + "`dyna subagent`" + ` is an alias for the same commands.

Session mode: start a worker session that behaves like a teammate.

A session is one harness session that survives across turns. It has no time
limit, never expires while idle (it stays open until closed), and has no turn
cap; the only limit is nesting depth (subagent.maxDepth). It delegates through
Dyna workflows and other Dyna subagents. Post to it with ` + "`dyna post @<address>`" + ` while
it works to steer it (delivered at its next tool boundary), or after its turn ends
to start a follow-up turn in the same conversation.

Workers inside a dyna workflow (workflow mode) can never start subagents.
Run ` + "`dyna guide`" + ` for when to use a subagent instead of a workflow.`,
	}
	cmd.AddCommand(
		startCommand(newManager), listCommand(newManager),
		showCommand(newManager), outputCommand(newManager),
		stopCommand(newManager), closeCommand(newManager), reopenCommand(newManager), promoteCommand(newManager), adoptCommand(newManager), renameCommand(newManager), consolidationCommand(newManager), transferCommand(newManager), historyCommand(newManager), turnCommand(newManager),
		dismissPendingCommand(newManager))
	return cmd
}

// ---- helpers ----

// textArg returns the argument, or the file it names when it starts with @.
func textArg(arg string) (string, error) {
	if strings.HasPrefix(arg, "@") && len(arg) > 1 {
		b, err := os.ReadFile(arg[1:])
		if err != nil {
			return "", fmt.Errorf("read %s: %w", arg[1:], err)
		}
		return string(b), nil
	}
	return arg, nil
}

func readSchema(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read schema: %w", err)
	}
	var probe any
	if err := json.Unmarshal(b, &probe); err != nil {
		return "", fmt.Errorf("schema %s is not valid JSON: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func progress(c *cobra.Command, format string, args ...any) {
	fmt.Fprintf(c.ErrOrStderr(), format+"\n", args...)
}

// resultText renders a turn's result for stdout: text as-is, JSON compact.
func resultText(t subagent.Turn) string {
	switch v := t.Result.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func printResult(c *cobra.Command, t subagent.Turn) {
	text := resultText(t)
	if text == "" {
		return
	}
	fmt.Fprint(c.OutOrStdout(), text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(c.OutOrStdout())
	}
}

// waitContext cancels on Ctrl-C so a blocked wait can be abandoned without
// affecting the session.
func waitContext(c *cobra.Command) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(c.Context(), syscall.SIGINT, syscall.SIGTERM)
}

func turnOutcome(info subagent.Info, t subagent.Turn) error {
	switch t.Status {
	case subagent.TurnOK:
		return nil
	case subagent.TurnStopped:
		return exitcode.New(2, fmt.Sprintf("turn %d of %s was stopped", t.N, info.ID))
	default:
		msg := t.Error
		if msg == "" {
			msg = "failed"
		}
		return fmt.Errorf("turn %d of %s %s: %s", t.N, info.ID, t.Status, msg)
	}
}

func turnEnvelope(info subagent.Info, t subagent.Turn) map[string]any {
	return map[string]any{
		"session": info.ID, "name": info.Name, "turn": t.N, "status": t.Status,
		"result": t.Result, "error": t.Error, "harnessSession": t.HarnessSession, "usage": t.Usage,
	}
}

// ---- start ----

func startCommand(newManager func() *subagent.Manager) *cobra.Command {
	var profileName, name, roomRef, dir, schemaPath, goal, under string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "start --goal <outcome; done when> --profile <name> [--name <label>] [--room <room>] [--dir <path>] [--schema <file>] [--under <goal>] <brief text | @file>",
		Short: "Start a subagent session; prints its address and runs the first turn in the background",
		Long:  "Start a subagent with a goal and brief. Use --under gN to select its goal. Otherwise, start uses the starter's unique open assigned item goal, then its single open held goal set by someone else. Multiple eligible goals require --under. With no eligible goal, the child holds a new standalone goal. Root holds no goals: a start from root's session gives the child a top-level goal of its own, unless --under names a goal an agent holds; a goal that root holds is refused.",
		// Start explains a missing brief better than cobra's argument count.
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			task := ""
			if len(args) == 1 {
				var err error
				if task, err = textArg(args[0]); err != nil {
					return err
				}
			}
			schema, err := readSchema(schemaPath)
			if err != nil {
				return err
			}
			// The CLI's own stamp and directory pick the room and the default
			// working directory; the engine takes the parent from the caller.
			rec, err := newManager().RoomStore().ResolveContext(roomRef, true)
			if err != nil {
				return err
			}
			if dir != "" {
				if dir, err = filepath.Abs(dir); err != nil {
					return err
				}
			}
			cwd, _ := os.Getwd()
			var info subagent.Info
			in := roomsvc.SubagentStart{Profile: profileName, Name: name, Dir: dir, Cwd: cwd, Schema: schema, Goal: goal, Under: under, Task: task, Owner: runstore.OwnerFromEnv()}
			if err := engineClient().Call(c.Context(), "StartSubagent", []any{rec.ID, in}, &info); err != nil {
				return err
			}
			if asJSON {
				out := map[string]any{
					"session": info.ID, "address": info.Address(), "name": info.Name,
					"room": info.Room, "roomId": info.RoomID, "goal": info.Goal,
					"profile": info.Profile, "depth": info.Depth, "turn": info.Turns,
				}
				if info.Item != "" {
					out["item"] = info.Item
				}
				return jsonout.Write(c.OutOrStdout(), out)
			}
			fmt.Fprintln(c.OutOrStdout(), info.Address())
			if info.Item != "" {
				progress(c, "assigned item %s on goal %s", info.Item, info.Goal)
			} else {
				progress(c, "holds goal %s", info.Goal)
			}
			progress(c, "running in background; post to it with `dyna post @%s <text>`, inspect it with `dyna subagent show %s`, watch live in the desktop app", info.Address(), info.Address())
			return nil
		},
	}
	cmd.Flags().StringVar(&goal, "goal", "", "required outcome and done condition; placed in an item or a standalone goal")
	cmd.Flags().StringVar(&under, "under", "", "goal ID to delegate from; default: the starter's open assigned item's goal, then its single held goal set by someone else; otherwise a standalone goal")
	cmd.Flags().StringVar(&profileName, "profile", "", "worker profile (default: the default profile)")
	cmd.Flags().StringVar(&name, "name", "", "label for this session, unique among open sessions in its room")
	cmd.Flags().StringVar(&roomRef, "room", "", "room name or ID (default: inherited room, else the current repo's room)")
	cmd.Flags().StringVar(&dir, "dir", "", "working directory (default: the parent session's directory when run from a session, else cwd)")
	cmd.Flags().StringVar(&schemaPath, "schema", "", "JSON Schema file: the first turn returns validated JSON")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

// ---- list / show ----

func tokens(u interface{ Total() int64 }) string {
	n := u.Total()
	switch {
	case n == 0:
		return "-"
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func listCommand(newManager func() *subagent.Manager) *cobra.Command {
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "list [--all]",
		Short: "List open sessions, including finished ones (--all includes closed ones)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			sessions, err := newManager().Store.ListSnapshot(true)
			if err != nil {
				return err
			}
			if !all {
				visible := sessions[:0]
				for _, info := range sessions {
					if info.Status != subagent.StatusClosed {
						visible = append(visible, info)
					}
				}
				sessions = visible
			}
			if asJSON {
				if sessions == nil {
					sessions = []subagent.Info{}
				}
				return jsonout.Write(c.OutOrStdout(), sessions)
			}
			byID := map[string]subagent.Info{}
			for _, s := range sessions {
				byID[s.ID] = s
			}
			for _, s := range sessions {
				parent := ""
				if s.ParentSession != "" {
					p := s.ParentSession
					if pi, ok := byID[p]; ok {
						p = pi.Address()
					}
					parent = "  parent " + p
				}
				fmt.Fprintf(c.OutOrStdout(), "%-30s %-12s %-8s depth %d  turns %d  tokens %s  active %s%s  session %s\n",
					s.Address(), s.Profile, s.Status, s.Depth, s.Turns, tokens(s.Usage), s.LastActivity.Format("2006-01-02 15:04:05"), parent, s.ID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include closed sessions")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return cmd
}

func showCommand(newManager func() *subagent.Manager) *cobra.Command {
	var asJSON, all, expand bool
	var last int
	cmd := &cobra.Command{
		Use:   "show <session>",
		Short: "Show a session: status, then the conversation (prompts, condensed tool activity, replies)",
		Long: "Show a session as a readable conversation: each turn's prompt, steering (with its delivery state),\n" +
			"tool activity condensed to one line per call, and the replies. Linked PRs show their state and role.\n" +
			"By default the last 3 turns.\n" +
			"--all shows every turn, --tools adds each call's command and output tail, --json is the\n" +
			"machine form, and `dyna subagent output <session> --raw` prints the exact harness output.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			manager := newManager()
			store := manager.Store
			info, err := store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			prs, err := manager.TrackedPRs(info.ID)
			if err != nil {
				return err
			}
			if asJSON {
				turns, _ := store.Turns(info.ID)
				return jsonout.Write(c.OutOrStdout(), map[string]any{"session": info, "turns": turns, "trackedPRs": prs})
			}
			n := last
			if all {
				n = 0
			}
			sess, err := transcript.NewLoader(store).Load(info.ID, n)
			if err != nil {
				return err
			}
			info = sess.Info
			w := c.OutOrStdout()
			fmt.Fprintf(w, "%s  (session %s)\n", info.Address(), info.ID)
			fmt.Fprintf(w, "  %s · %s · depth %d", info.Status, info.Profile, info.Depth)
			if info.ParentSession != "" {
				fmt.Fprintf(w, " under %s", info.ParentSession)
			}
			turns, err := store.Turns(info.ID)
			if err != nil {
				return err
			}
			usageUnavailable := info.Usage.Total() == 0
			completed := false
			for _, turn := range turns {
				if !turn.Done() {
					continue
				}
				completed = true
				n := turn.UsageNormalization
				if n.Version == 0 || n.Input != "unknown" || n.CachedInput != "unknown" || n.Output != "unknown" {
					usageUnavailable = false
					break
				}
			}
			usageUnavailable = usageUnavailable && completed
			fmt.Fprintf(w, " · %d turn%s", info.Turns, plural(info.Turns))
			if usageUnavailable {
				fmt.Fprint(w, " · usage unavailable")
			} else {
				fmt.Fprintf(w, " · tokens in %d (cached %d) out %d", info.Usage.InputTokens, info.Usage.CachedTokens, info.Usage.OutputTokens)
			}
			if info.Usage.CostUSD > 0 {
				fmt.Fprintf(w, " · $%.4f", info.Usage.CostUSD)
			}
			fmt.Fprintf(w, "\n  %s\n  created %s · active %s\n", info.Dir, info.Created.Format("2006-01-02 15:04:05"), info.LastActivity.Format("2006-01-02 15:04:05"))
			if info.Status == subagent.StatusDormant {
				fmt.Fprintf(w, "  finished %s; dormant until a coordination post wakes it\n", info.Finished.Format("2006-01-02 15:04:05"))
			}
			if info.HarnessSession != "" {
				fmt.Fprintf(w, "  harness session %s (resumable %v)\n", info.HarnessSession, info.Resumable)
			}
			for _, pr := range prs {
				status := pr.State
				if !pr.Watching && status == "open" {
					status = "untracked"
				}
				if pr.Kind == "local" {
					fmt.Fprintf(w, "  Change %s · %s · %s\n", pr.Key, status, strings.Join(pr.Roles, ", "))
				} else {
					fmt.Fprintf(w, "  PR #%d %s · %s · %s\n", pr.Number, pr.URL, status, strings.Join(pr.Roles, ", "))
				}
			}
			if skipped := info.Turns - len(sess.Turns); skipped > 0 {
				fmt.Fprintf(w, "\n(%d earlier turn%s not shown; --all shows every turn)\n", skipped, plural(skipped))
			}
			for _, t := range sess.Turns {
				fmt.Fprintln(w)
				transcript.WriteTurn(w, t, transcript.PlainOptions{Agent: info.Profile, Expand: expand})
			}
			if n := len(sess.Turns); n > 0 && sess.Turns[n-1].Running() {
				fmt.Fprintf(w, "\n… working: %s\n", transcript.Activity(sess.Turns[n-1]))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&all, "all", false, "show every turn")
	cmd.Flags().IntVar(&last, "turns", 3, "show the last N turns")
	cmd.Flags().BoolVar(&expand, "tools", false, "include each tool call's command and output tail")
	return cmd
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---- output ----

func outputCommand(newManager func() *subagent.Manager) *cobra.Command {
	var turn int
	var raw, asJSON, conv bool
	cmd := &cobra.Command{
		Use:   "output <session> [--turn N] [--transcript | --raw | --json]",
		Short: "Print a turn's final text (or validated JSON); --transcript the readable turn; --raw the harness's exact stdout",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			store := newManager().Store
			info, err := store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			n := turn
			if n == 0 {
				n = info.Turns
			}
			if n <= 0 || n > info.Turns {
				return fmt.Errorf("session %s has no turn %d (it has %d)", info.ID, n, info.Turns)
			}
			t, err := store.ReadTurn(info.ID, n)
			if err != nil {
				return err
			}
			if raw {
				path, _ := store.RawOutputPath(info.ID)
				return copyRange(c.OutOrStdout(), path, t.RawStart, t.RawEnd)
			}
			if conv {
				sess, err := transcript.NewLoader(store).Load(info.ID, 0)
				if err != nil {
					return err
				}
				for _, ct := range sess.Turns {
					if ct.N == n {
						transcript.WriteTurn(c.OutOrStdout(), ct, transcript.PlainOptions{Agent: info.Profile})
					}
				}
				return nil
			}
			if !t.Done() {
				return fmt.Errorf("turn %d of %s is still %s; its report will reach the owner and also alert its current available parent if it fails — inspect with `dyna subagent show %s`", t.N, info.Address(), t.Status, info.Address())
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), turnEnvelope(info, t))
			}
			printResult(c, t)
			return turnOutcome(info, t)
		},
	}
	cmd.Flags().IntVar(&turn, "turn", 0, "turn number (default: the latest)")
	cmd.Flags().BoolVar(&raw, "raw", false, "the harness's raw stdout for the turn, exactly")
	cmd.Flags().BoolVar(&conv, "transcript", false, "the turn as a readable conversation (prompt, condensed tool activity, replies)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable envelope")
	return cmd
}

func copyRange(w io.Writer, path string, start, end int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	var r io.Reader = f
	if end > start {
		r = io.LimitReader(f, end-start)
	}
	_, err = io.Copy(w, r)
	return err
}

// ---- stop / close ----

func stopCommand(newManager func() *subagent.Manager) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <session>",
		Short: "Stop the running turn (the session stays open and resumable)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx, stop := waitContext(c)
			defer stop()
			store := newManager().Store
			info, err := store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			if err := engineClient().Call(ctx, "StopSession", []any{info.RoomID, info.ID}, nil); err != nil {
				return err
			}
			t, err := store.ReadTurn(info.ID, info.Turns)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "stopped turn %d (%s); `dyna post @%s <text>` resumes the session\n", t.N, t.Status, args[0])
			return nil
		},
	}
}

func promoteCommand(newManager func() *subagent.Manager) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "promote <session>",
		Short: "Make a session a top-level lead without stopping its work",
		Long:  "Detach an open session from its parent and set its depth to 1. Its children stay attached, with updated depths. All history and active work stay intact. A top-level session is unchanged.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			store := newManager().Store
			info, err := store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			if err := engineClient().Call(c.Context(), "PromoteSession", []any{info.RoomID, info.ID}, nil); err != nil {
				return err
			}
			if info, err = store.ResolveSnapshot(info.ID); err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), info)
			}
			fmt.Fprintf(c.OutOrStdout(), "promoted %s (%s) to a top-level lead at depth %d\n", info.Address(), info.ID, info.Depth)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable session")
	return cmd
}

func adoptCommand(newManager func() *subagent.Manager) *cobra.Command {
	var under string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "adopt <session> --under <parent>",
		Short: "Move a session and its descendants under another session",
		Long:  "Move an open session, with its descendants, under another open session in the same room. Depths follow the new parent. A move that makes a cycle or passes subagent.maxDepth is refused. History, goals, PR roles, worktrees and active work stay intact. The old and the new parent each get one notice. Root, the owner or the session's current parent may run it. promote is the inverse.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			store := newManager().Store
			info, err := store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			if err := engineClient().Call(c.Context(), "AdoptSession", []any{info.RoomID, info.ID, under}, nil); err != nil {
				return err
			}
			if info, err = store.ResolveSnapshot(info.ID); err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), info)
			}
			fmt.Fprintf(c.OutOrStdout(), "moved %s (%s) under %s at depth %d\n", info.Address(), info.ID, under, info.Depth)
			return nil
		},
	}
	cmd.Flags().StringVar(&under, "under", "", "the new parent session (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable session")
	_ = cmd.MarkFlagRequired("under")
	return cmd
}

func renameCommand(newManager func() *subagent.Manager) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "rename <session> <new-name>",
		Short: "Change a session's label without changing its ID or history",
		Long:  "Give an open session a new label. Its ID, history, goals and PR links stay the same, and the room members list shows the new address. The new name must not be used by another open session in the room, and service names such as library are reserved. Root, the owner, the session itself or its parent may run it.",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			store := newManager().Store
			info, err := store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			if err := engineClient().Call(c.Context(), "RenameSession", []any{info.RoomID, info.ID, args[1]}, nil); err != nil {
				return err
			}
			if info, err = store.ResolveSnapshot(info.ID); err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), info)
			}
			fmt.Fprintf(c.OutOrStdout(), "renamed %s (%s) to %s\n", args[0], info.ID, info.Address())
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable session")
	return cmd
}

func closeCommand(newManager func() *subagent.Manager) *cobra.Command {
	var force, keepWorktrees bool
	cmd := &cobra.Command{
		Use:   "close <session>",
		Short: "End the session: stop any running turn and accept no further turns",
		Long:  "End a session and stop any running turn. A session's own turn that ends with no responsibilities makes it dormant, so a session cannot close itself. Close transfers PR authorship to the nearest open ancestor, or root, and hands held goals to the nearest open agent. Root and the owner hold no goals, so when only they are left above the session, close is refused until each held goal is done, dropped, or transferred to an open agent; the refusal names the commands. If only the owner can unblock the work, root asks the owner first with `dyna ask`, and a forced close does not move the goals to the owner. Reviewer roles return to the pool. Live workflow runs are canceled. A failed handoff keeps the session open; --force closes it anyway with its other unresolved work. Open questions are withdrawn and non-kept shell jobs stop. Children that own nothing close too; the rest move to the nearest open ancestor. --keep-worktrees locks the session's worktrees after its turn stops and before the close commits, so a manual handoff keeps them: cleanup keeps a locked tree until git worktree unlock releases it, and a lock that fails leaves the session open.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx, stop := waitContext(c)
			defer stop()
			info, err := newManager().Store.ResolveSnapshot(args[0])
			if err != nil {
				return err
			}
			// The engine applies the choices: it checks who may close, stops a
			// running turn, then keeps the worktrees, so a refused close never
			// changes a lock.
			opts := roomsvc.CloseOptions{Force: force, KeepWorktrees: keepWorktrees}
			var result roomsvc.Member
			if err := engineClient().Call(ctx, "CloseSessionOptions", []any{info.RoomID, info.ID, opts}, &result); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "closed %s (%s)\n", info.Address(), info.ID)
			for _, item := range result.CloseSummary {
				fmt.Fprintln(c.OutOrStdout(), item)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "close even if a responsibility handoff fails")
	cmd.Flags().BoolVar(&keepWorktrees, "keep-worktrees", false, "lock the session's worktrees so cleanup keeps them, as a manual handoff needs")
	return cmd
}

// ---- hidden runner ----

func turnCommand(newManager func() *subagent.Manager) *cobra.Command {
	return &cobra.Command{
		Use:    "_turn <session> <turn>",
		Short:  "internal: run one turn (launched detached by start/msg)",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			var n int
			if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil || n < 1 {
				return fmt.Errorf("invalid turn %q", args[1])
			}
			var fd int
			if _, err := fmt.Sscanf(os.Getenv(subagent.TurnLockFDEnv), "%d", &fd); err != nil || fd < 3 {
				return fmt.Errorf("%s is not set; `dyna subagent _turn` is launched by `dyna subagent start` or `dyna post`", subagent.TurnLockFDEnv)
			}
			syscall.CloseOnExec(fd)
			_ = os.Unsetenv(subagent.TurnLockFDEnv)
			lock := os.NewFile(uintptr(fd), "turn.lock")
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			m := newManager()
			// The detached runner writes its session record through the engine.
			m.Engine = engineclient.New("")
			return m.RunTurn(ctx, args[0], n, lock)
		},
	}
}

// engineClient reaches the engine, which applies session changes and checks
// the caller (requirement 2). Tests serve a fake engine (serveSessionControls)
// on their isolated XDG_RUNTIME_DIR.
var engineClient = func() *engineclient.Client { return engineclient.New("") }
