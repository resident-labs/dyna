package hooks

import (
	"fmt"
	"os"

	"dyna-agent/internal/hookinstall"
	"github.com/spf13/cobra"
)

func addInstallCommands(cmd *cobra.Command) {
	var main, name, toolPath string
	start := &cobra.Command{Use: "_install", Short: "Start a self-update installation unit", Long: "Start an independent user service for a captured self-update hook run.", Hidden: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if main == "" || name == "" || toolPath == "" {
			return fmt.Errorf("install requires --main, --name, and --tool-path")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		j := hookinstall.Job{ID: os.Getenv("DYNA_HOOK_RUN_ID"), SHA: os.Getenv("DYNA_HOOK_SHA"), Repo: os.Getenv("DYNA_HOOK_REPO"), Hook: name, Session: os.Getenv("DYNA_HOOK_SESSION"), HookLog: os.Getenv("DYNA_HOOK_LOG"), Config: hookinstall.DefaultConfig(main, exe)}
		j.Config.Path = toolPath
		return hookinstall.New().Start(c.Context(), j, c.OutOrStdout())
	}}
	start.Flags().StringVar(&toolPath, "tool-path", "", "captured build tool search path")
	start.Flags().StringVar(&main, "main", "", "main checkout path")
	start.Flags().StringVar(&name, "name", "", "hook name")
	cmd.AddCommand(start)
	for _, verb := range []string{"_install-worker", "_install-locked"} {
		locked := verb == "_install-locked"
		var root string
		command := &cobra.Command{Use: verb + " <job-id>", Short: "Run an installation worker", Long: "Process a stored self-update job in its independent installation unit.", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
			s := hookinstall.New()
			if root != "" {
				s.Jobs.Root = root
			}
			if locked {
				return s.Locked(c.Context(), a[0], os.Getenv("DYNA_HOOK_LOG"), c.OutOrStdout())
			}
			return s.Worker(c.Context(), a[0])
		}}
		command.Flags().StringVar(&root, "jobs-root", "", "installation receipt directory")
		cmd.AddCommand(command)
	}
}
