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
	cmd := &cobra.Command{Use: "hooks", Short: "Manage trusted repository lifecycle hooks", Long: "View and manage trusted shell commands for repository events. Hooks are shared by this repository and stored outside its files. Read dyna guide git before changing them."}
	cmd.PersistentFlags().StringVar(&repo, "repo", ".", "repository path")
	cmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		var err error
		repo, err = filepath.Abs(repo)
		return err
	}
	list := &cobra.Command{Use: "list", Short: "List hook settings and audit records", Long: "Print all hooks for this repository as JSON, including commands, events, and who added or edited them.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		var h []hooks.Hook
		e := engineclient.New("").Call(c.Context(), "ListHooks", []any{repo}, &h)
		if e != nil {
			return e
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(h)
	}}
	var h hooks.Hook
	var timeout time.Duration
	var recipe string
	add := &cobra.Command{Use: "add [name]", Short: "Add a repository event hook", Long: "Save a trusted shell command for a repository event. Use --recipe doctor for a schedule hook that examines the machine every minute while load or memory is high, or --recipe self-update for the Dyna-only Update Dyna recipe. Agents must tell the owner when they add or change a hook.", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if len(a) == 0 && recipe == "" {
			return fmt.Errorf("hook name is required")
		}
		name := recipe
		if len(a) > 0 {
			name = a[0]
		}
		if recipe != "" {
			r, ok := hooks.Recipes[recipe]
			if !ok {
				return fmt.Errorf("unknown hook recipe %q; built-in recipes are %s", recipe, hooks.RecipeHelp())
			}
			for _, flag := range []string{"event", "command", "cwd", "timeout", "on-fail", "every-minutes"} {
				if c.Flags().Changed(flag) {
					return fmt.Errorf("--recipe cannot be combined with --%s", flag)
				}
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			h, err = r.Build(c.Context(), repo, exe, name)
			if err != nil {
				return err
			}
		} else {
			h.Name = name
			h.Timeout = timeout.String()
		}
		return engineclient.New("").Call(c.Context(), "AddHook", []any{repo, h}, nil)
	}}
	add.Flags().StringVar(&recipe, "recipe", "", "built-in recipe: "+hooks.RecipeHelp())
	add.Flags().StringVar(&h.Event, "event", "", "lifecycle event")
	add.Flags().StringVar(&h.Command, "command", "", "trusted shell command")
	add.Flags().StringVar(&h.Cwd, "cwd", "worktree", "worktree, main, or an absolute path")
	add.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "command timeout")
	add.Flags().StringVar(&h.OnFail, "on-fail", "alert", "alert, or block for before-land")
	add.Flags().IntVar(&h.EveryMinutes, "every-minutes", 0, "schedule interval in minutes")
	rm := &cobra.Command{Use: "rm <name>", Short: "Remove a repository hook", Long: "Remove the named hook from this repository. Existing run logs remain available.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		return engineclient.New("").Call(c.Context(), "RemoveHook", []any{repo, a[0]}, nil)
	}}
	cmd.AddCommand(list, add, rm, editCommand(&repo))
	for _, verb := range []string{"run", "test"} {
		sample := verb == "test"
		cmd.AddCommand(&cobra.Command{Use: verb + " <name>", Short: map[bool]string{true: "Test a hook with sample event values", false: "Run a hook with current Git values"}[sample], Long: map[bool]string{true: "Run the real hook command with sample branch, SHA, and PR values. Keep the real caller identity. This command has real effects and waits for the result.", false: "Run the real hook command now with current Git values. Wait for the result and save its log."}[sample], Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
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
		}})
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
	cmd := &cobra.Command{Use: "edit <name>", Short: "Change selected hook settings", Long: "Change only the supplied fields of an existing repository hook. Keep its creator and creation time, and record who edited it and when. Agents must tell the owner about the change.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
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
		if !changed {
			return fmt.Errorf("supply at least one field to edit")
		}
		return engineclient.New("").Call(c.Context(), "EditHook", []any{*repo, args[0], patch}, nil)
	}}
	cmd.Flags().StringVar(&h.Command, "command", "", "trusted shell command")
	cmd.Flags().StringVar(&h.Event, "event", "", "lifecycle event")
	cmd.Flags().StringVar(&h.Cwd, "cwd", "", "worktree, main, or an absolute path")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "command timeout")
	cmd.Flags().StringVar(&h.OnFail, "on-fail", "", "alert, or block for before-land")
	cmd.Flags().IntVar(&h.EveryMinutes, "every-minutes", 0, "schedule interval in minutes")
	return cmd
}
