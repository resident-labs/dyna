package worktrees

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktreesvc"
)

var engineClient = func() *engineclient.Client { return engineclient.New("") }

type listedWorktree struct {
	worktrees.Record
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// NewCommand builds the retained-worktree discovery and removal command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "worktrees",
		Aliases: []string{"worktree"},
		Short:   "Discover and safely remove retained worktrees",
		Args:    cobra.NoArgs,
		RunE:    func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	cmd.AddCommand(listCommand(), newCommand(), envCommand(), gcCommand(), removeCommand(), pruneCommand())
	return cmd
}

func listCommand() *cobra.Command {
	var runID, repo string
	var asJSON, check, orphaned bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repository worktrees, or retained worktrees with --run or --check",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if orphaned && (c.Flags().Changed("run") || check) {
				return fmt.Errorf("--orphaned cannot be combined with --run or --check")
			}
			if orphaned || c.Flags().Changed("repo") || (!c.Flags().Changed("run") && !check) {
				if c.Flags().Changed("repo") && c.Flags().Changed("run") {
					return fmt.Errorf("--repo and --run cannot be combined")
				}
				var trees []worktrees.Tree
				var err error
				if orphaned {
					trees, err = worktrees.Orphaned(c.Context(), repo)
				} else {
					trees, err = worktrees.Scan(c.Context(), repo)
				}
				if err != nil {
					return err
				}
				if asJSON {
					return jsonout.Write(c.OutOrStdout(), trees)
				}
				return printTrees(c.OutOrStdout(), trees)
			}
			records, warnings, err := worktrees.ListWithWarnings()
			if err != nil {
				return err
			}
			printWarnings(c.ErrOrStderr(), warnings)
			out := make([]listedWorktree, 0, len(records))
			for _, rec := range records {
				if runID != "" && rec.RunID != runID {
					continue
				}
				item := listedWorktree{Record: rec}
				if check {
					item.State, err = worktrees.Inspect(rec)
					if err != nil {
						item.State = worktrees.StateError
						item.Error = err.Error()
						printWarnings(c.ErrOrStderr(), []error{fmt.Errorf("inspect %s: %w", rec.Path, err)})
					}
				}
				out = append(out, item)
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), out)
			}
			for _, item := range out {
				state := ""
				if check {
					state = "  " + item.State
				}
				fmt.Fprintf(c.OutOrStdout(), "%s  %s  %s  %s  %s  %s%s\n", item.Path, item.RunID, item.Label, formatPort(item.Port), item.Repo, formatAge(item.RetainedAt), state)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&runID, "run", "", "only worktrees attributed to this run id")
	cmd.Flags().StringVar(&repo, "repo", ".", "repository path (defaults to the current directory)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	cmd.Flags().BoolVar(&check, "check", false, "inspect current HEAD and status")
	cmd.Flags().BoolVar(&orphaned, "orphaned", false, "only kept worktrees whose owning sessions are closed")
	return cmd
}

func newCommand() *cobra.Command {
	var repo, base string
	var noDeps bool
	cmd := &cobra.Command{
		Use:   "new <branch>",
		Short: "Create a managed worktree and print its path",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			repoPath, err := filepath.Abs(repo)
			if err != nil {
				return err
			}
			var result worktreesvc.NewResult
			if err := engineClient().Call(c.Context(), "NewWorktree", []any{repoPath, args[0], base, noDeps}, &result); err != nil {
				return fmt.Errorf("the worktree was not created: %w", err)
			}
			for _, notice := range result.Notices {
				fmt.Fprintln(c.ErrOrStderr(), notice)
			}
			if _, err := fmt.Fprintln(c.OutOrStdout(), result.Record.Path); err != nil {
				return err
			}
			// The tree exists even when a hook fails, so the path prints first.
			if err := result.Err(); err != nil {
				return fmt.Errorf("the worktree was created at %s, but a worktree-created hook failed: %w", result.Record.Path, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", ".", "repository path (defaults to the current directory)")
	cmd.Flags().StringVar(&base, "base", "", "base revision (default: origin/HEAD, then current branch)")
	cmd.Flags().BoolVar(&noDeps, "no-deps", false, "skip restoring node_modules in the new worktree")
	return cmd
}

func envCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "env [path]",
		Short: "Print export lines for a worktree's port block",
		Long:  "Print export lines for a worktree's port block. The path defaults to the current directory.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			rec, err := worktrees.ForDir(c.Context(), dir)
			if err != nil {
				return err
			}
			env := worktrees.PortEnv(rec)
			if env == nil {
				return fmt.Errorf("worktree %s has no port block", rec.Path)
			}
			keys := make([]string, 0, len(env))
			for key := range env {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if _, err := fmt.Fprintf(c.OutOrStdout(), "export %s=%s\n", key, env[key]); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func formatPort(port int) string {
	if port == 0 {
		return "port none"
	}
	return fmt.Sprintf("port %d", port)
}

func gcCommand() *cobra.Command {
	var repo, session string
	var dryRun, asJSON bool
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove safe, merged worktrees with closed or old unknown owners",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			repoPath, err := filepath.Abs(repo)
			if err != nil {
				return err
			}
			var result worktreesvc.GCResult
			if err := engineClient().Call(c.Context(), "GCWorktrees", []any{repoPath, session, dryRun}, &result); err != nil {
				return fmt.Errorf("worktree cleanup failed: %w", err)
			}
			trees := result.Trees
			if asJSON {
				if writeErr := jsonout.Write(c.OutOrStdout(), trees); writeErr != nil {
					return writeErr
				}
			} else {
				for _, tree := range trees {
					if _, writeErr := fmt.Fprintf(c.OutOrStdout(), "%s %s (%s)\n", tree.Action, tree.Path, tree.Reason); writeErr != nil {
						return writeErr
					}
				}
			}
			return result.Err()
		},
	}
	cmd.Flags().StringVar(&repo, "repo", ".", "repository path (defaults to the current directory)")
	cmd.Flags().StringVar(&session, "session", "", "only worktrees owned by this session id")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print decisions without removing anything")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable decisions")
	return cmd
}

func printTrees(w io.Writer, trees []worktrees.Tree) error {
	if _, err := fmt.Fprintln(w, "PATH\tBRANCH\tOWNER\tMERGED\tDIRTY\tSIZE (bytes)\tAGE"); err != nil {
		return err
	}
	for _, tree := range trees {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%d\t%s\n", tree.Path, tree.Branch, tree.Owner, tree.Merged, tree.Dirty, tree.Size, tree.Age.Round(time.Second)); err != nil {
			return err
		}
	}
	return nil
}

func removeCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "remove <name|path>...",
		Aliases: []string{"rm"},
		Short:   "Remove registered worktrees that are unchanged since retention",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			names := make([]string, len(args))
			for i, name := range args {
				// Preserve basename lookup, but resolve paths in the caller checkout.
				if name == "." || name == ".." || filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
					var err error
					names[i], err = filepath.Abs(name)
					if err != nil {
						return err
					}
				} else {
					names[i] = name
				}
			}
			var result worktreesvc.RemoveResult
			if err := engineClient().Call(c.Context(), "RemoveWorktrees", []any{names, force}, &result); err != nil {
				return fmt.Errorf("worktree removal failed: %w", err)
			}
			for _, line := range result.Lines {
				fmt.Fprintln(c.OutOrStdout(), line)
			}
			return result.Err()
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove even when changed since retention")
	return cmd
}

func pruneCommand() *cobra.Command {
	var olderThan string
	var force, dryRun, asJSON bool
	cmd := &cobra.Command{
		Use:   "prune --older-than <duration>",
		Short: "Remove old or orphaned retained worktrees when safe",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			age, err := time.ParseDuration(strings.TrimSpace(olderThan))
			if err != nil || age < 0 {
				return fmt.Errorf("--older-than must be a non-negative duration")
			}
			var result worktreesvc.PruneResult
			if err := engineClient().Call(c.Context(), "PruneWorktrees", []any{age, force, dryRun}, &result); err != nil {
				return fmt.Errorf("worktree pruning failed: %w", err)
			}
			for _, warning := range result.Warnings {
				fmt.Fprintln(c.ErrOrStderr(), "warning: "+warning)
			}
			decisions := result.Decisions
			if asJSON {
				if err := jsonout.Write(c.OutOrStdout(), decisions); err != nil {
					return err
				}
			} else {
				printPrune(c.OutOrStdout(), decisions, dryRun)
			}
			return result.Err()
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "minimum age such as 24h or 168h")
	cmd.Flags().BoolVar(&force, "force", false, "remove modified worktrees too")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print decisions without removing anything")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable decisions")
	return cmd
}

func printPrune(w io.Writer, decisions []worktreesvc.PruneDecision, dryRun bool) {
	for _, decision := range decisions {
		prefix := decision.Action
		if dryRun && decision.Action == worktrees.ActionRemoved {
			prefix = "would remove"
		}
		if decision.Action == worktrees.ActionSkipped {
			fmt.Fprintf(w, "%s %s (%s)\n", prefix, decision.Path, decision.State)
		} else {
			fmt.Fprintf(w, "%s %s\n", prefix, decision.Path)
		}
	}
}

func printWarnings(w io.Writer, warnings []error) {
	for _, warning := range warnings {
		fmt.Fprintln(w, "warning: "+warning.Error())
	}
}

func formatAge(retainedAt time.Time) string {
	if retainedAt.IsZero() {
		return "unknown"
	}
	age := time.Since(retainedAt)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String()
}
