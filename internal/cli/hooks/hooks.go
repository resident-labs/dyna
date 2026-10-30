// Package hooks exposes lifecycle hooks. The singular hook command is separate.
package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hooks"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	var repo string
	var listGlobal, addGlobal, rmGlobal bool
	cmd := &cobra.Command{Use: "hooks", Short: "Manage trusted repository lifecycle hooks", Long: "View and manage trusted shell commands for repository events. Hooks are shared by this repository and stored outside its files. Read dyna guide git before changing them."}
	cmd.PersistentFlags().StringVar(&repo, "repo", ".", "repository path")
	cmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		var err error
		repo, err = filepath.Abs(repo)
		return err
	}
	list := &cobra.Command{Use: "list", Short: "List hook settings and audit records", Long: "Print all hooks for this repository as JSON, including commands, events, and who added or edited them. --global prints the hooks you added for the whole machine. Built-in passes are services: see `dyna services list`.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var h []hooks.Hook
		method, args := "ListHooks", []any{repo}
		if listGlobal {
			method, args = "ListGlobalHooks", nil
		}
		e := engineclient.New("").Call(c.Context(), method, args, &h)
		if e != nil {
			return e
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(h)
	}}
	list.Flags().BoolVar(&listGlobal, "global", false, "list the hooks you added for the whole machine")
	var h hooks.Hook
	var timeout time.Duration
	add := &cobra.Command{Use: "add <name>", Short: "Add a repository event hook", Long: "Save a trusted shell command for a repository event. --global saves a hook that belongs to the machine and needs an absolute working directory. Agents must tell the owner when they add or change a hook.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		h.Name = a[0]
		h.Timeout = timeout.String()
		if addGlobal {
			return engineclient.New("").Call(c.Context(), "AddGlobalHook", []any{h}, nil)
		}
		return engineclient.New("").Call(c.Context(), "AddHook", []any{repo, h}, nil)
	}}
	add.Flags().BoolVar(&addGlobal, "global", false, "add a hook that belongs to the machine, not one repository")
	add.Flags().StringVar(&h.Event, "event", "", "lifecycle event")
	add.Flags().StringVar(&h.Command, "command", "", "trusted shell command")
	add.Flags().StringVar(&h.Cwd, "cwd", "worktree", "worktree, main, or an absolute path")
	add.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "command timeout")
	add.Flags().StringVar(&h.OnFail, "on-fail", "alert", "alert, or block for before-land")
	add.Flags().IntVar(&h.EveryMinutes, "every-minutes", 0, "schedule interval in minutes")
	rm := &cobra.Command{Use: "rm <name>", Short: "Remove a repository hook", Long: "Remove the named hook from this repository. Existing run logs remain available. A built-in hook cannot be removed; turn it off instead.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if rmGlobal {
			return engineclient.New("").Call(c.Context(), "RemoveGlobalHook", []any{a[0]}, nil)
		}
		return engineclient.New("").Call(c.Context(), "RemoveHook", []any{repo, a[0]}, nil)
	}}
	rm.Flags().BoolVar(&rmGlobal, "global", false, "remove a hook that belongs to the machine")
	cmd.AddCommand(list, add, rm, editCommand(&repo))
	for _, verb := range []string{"run", "test"} {
		sample := verb == "test"
		var runGlobal bool
		command := &cobra.Command{Use: verb + " <name>", Short: map[bool]string{true: "Test a hook with sample event values", false: "Run a hook with current Git values"}[sample], Long: map[bool]string{true: "Run the real hook command with sample branch, SHA, and PR values. Keep the real caller identity. This command has real effects and waits for the result.", false: "Run the real hook command now with current Git values. Wait for the result and save its log. --global runs a hook you added for the whole machine. Built-in passes are services: run them with `dyna services run`."}[sample], Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
			if runGlobal {
				var ev hooks.Run
				err := engineclient.New("").Call(c.Context(), "RunGlobalHook", []any{a[0]}, &ev)
				if ev.ID != "" {
					fmt.Fprintf(c.OutOrStdout(), "%s %s %s\n", ev.Name, ev.Result, ev.LogPath)
				}
				return err
			}
			method := "RunHook"
			if sample {
				method = "TestHook"
			}
			var ev hooks.Run
			err := engineclient.New("").Call(c.Context(), method, []any{repo, a[0]}, &ev)
			if ev.ID != "" {
				fmt.Fprintf(c.OutOrStdout(), "%s %s %s\n", ev.Name, ev.Result, ev.LogPath)
			}
			return err
		}}
		if !sample {
			command.Flags().BoolVar(&runGlobal, "global", false, "run a hook you added for the whole machine")
		}
		cmd.AddCommand(command)
	}
	log := &cobra.Command{Use: "log [name]", Short: "Read hook results and the latest log", Long: "List hook attempts for this repository. Supply a name to print its latest log after the results. Reconcile pending self-update results first.", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := engineclient.New("").Call(c.Context(), "ReconcileHookInstall", nil, nil); err != nil {
			return err
		}
		name := ""
		if len(a) > 0 {
			name = a[0]
		}
		var runs []hooks.Run
		err := engineclient.New("").Call(c.Context(), "HookRuns", []any{repo, name}, &runs)
		if err != nil {
			return err
		}
		for _, r := range runs {
			fmt.Fprintf(c.OutOrStdout(), "%s %s %s %s\n", r.StartedAt.Format(time.RFC3339), r.Name, r.Result, r.LogPath)
		}
		if len(a) > 0 && len(runs) > 0 {
			b, err := os.ReadFile(runs[len(runs)-1].LogPath)
			if err != nil {
				return err
			}
			_, err = c.OutOrStdout().Write(b)
			return err
		}
		return nil
	}}
	cmd.AddCommand(log)
	addInstallCommands(cmd)
	return cmd
}

func editCommand(repo *string) *cobra.Command {
	var h hooks.Hook
	var timeout time.Duration
	var disabled, editGlobal bool
	cmd := &cobra.Command{Use: "edit <name>", Short: "Change selected hook settings", Long: "Change only the supplied fields of an existing repository hook. Keep its creator and creation time, and record who edited it and when. --global edits a hook you added for the whole machine, or the interval of a built-in service pass. Agents must tell the owner about the change.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		patch := hooks.Patch{}
		changed := false
		for _, field := range []struct {
			name   string
			value  *string
			target **string
		}{
			{"command", &h.Command, &patch.Command}, {"event", &h.Event, &patch.Event}, {"cwd", &h.Cwd, &patch.Cwd}, {"on-fail", &h.OnFail, &patch.OnFail},
		} {
			if c.Flags().Changed(field.name) {
				*field.target = field.value
				changed = true
			}
		}
		if c.Flags().Changed("timeout") {
			value := timeout.String()
			patch.Timeout = &value
			changed = true
		}
		if c.Flags().Changed("every-minutes") {
			patch.EveryMinutes = &h.EveryMinutes
			changed = true
		}
		if c.Flags().Changed("disabled") {
			patch.Disabled = &disabled
			changed = true
		}
		if !changed {
			return fmt.Errorf("supply at least one field to edit")
		}
		if editGlobal {
			return engineclient.New("").Call(c.Context(), "EditGlobalHook", []any{args[0], patch}, nil)
		}
		return engineclient.New("").Call(c.Context(), "EditHook", []any{*repo, args[0], patch}, nil)
	}}
	cmd.Flags().BoolVar(&editGlobal, "global", false, "edit a hook that belongs to the machine")
	cmd.Flags().StringVar(&h.Command, "command", "", "trusted shell command")
	cmd.Flags().StringVar(&h.Event, "event", "", "lifecycle event")
	cmd.Flags().StringVar(&h.Cwd, "cwd", "", "worktree, main, or an absolute path")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "command timeout")
	cmd.Flags().StringVar(&h.OnFail, "on-fail", "", "alert, or block for before-land")
	cmd.Flags().IntVar(&h.EveryMinutes, "every-minutes", 0, "schedule interval in minutes")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "keep the hook and stop automatic runs; --disabled=false turns it back on")
	return cmd
}
