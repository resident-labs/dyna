// dyna: harness-agnostic dynamic multi-agent workflows.
// CLI for agents (claude-code, codex, antigravity, opencode, muse, grok, pi); humans use the desktop app.
package main

import (
	"context"
	"dyna-agent/internal/ghclient"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/cli/ask"
	"dyna-agent/internal/cli/call"
	"dyna-agent/internal/cli/computer"
	clidoctor "dyna-agent/internal/cli/doctor"
	cligit "dyna-agent/internal/cli/git"
	"dyna-agent/internal/cli/goal"
	"dyna-agent/internal/cli/hook"
	"dyna-agent/internal/cli/interactive"
	"dyna-agent/internal/cli/journal"
	clilibrary "dyna-agent/internal/cli/library"
	climaintenance "dyna-agent/internal/cli/maintenance"
	"dyna-agent/internal/cli/managed"
	"dyna-agent/internal/cli/profiles"
	cliroom "dyna-agent/internal/cli/room"
	cliroot "dyna-agent/internal/cli/root"
	"dyna-agent/internal/cli/rules"
	cliruntime "dyna-agent/internal/cli/runtime"
	clisettings "dyna-agent/internal/cli/settings"
	"dyna-agent/internal/cli/shell"
	clisubagent "dyna-agent/internal/cli/subagent"
	cliupdate "dyna-agent/internal/cli/update"
	"dyna-agent/internal/cli/usage"
	versioncmd "dyna-agent/internal/cli/version"
	"dyna-agent/internal/cli/workflows"
	"dyna-agent/internal/computerrestart"
	"dyna-agent/internal/engine"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/harness/home"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/store"
	"dyna-agent/internal/userservices"
)

// version is stamped from the release tag with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	callerid.WaitForLaunch()
	// The Settings sign-in runs this in a private harness HOME. It needs that
	// HOME, so it runs before RestoreOwner and before the root command, whose
	// stores cannot open a data folder inside a harness home.
	if harnessruntime.IsOpenCodeGoSignInCommand(os.Args[1:]) {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := harnessruntime.OpenCodeGoSignInCommand(ctx, os.Args[1:])
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		return
	}
	if harnessruntime.IsPiSignInCommand(os.Args[1:]) {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := harnessruntime.PiSignInCommand(ctx, os.Args[1:])
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		return
	}
	if harnessruntime.IsACPSignInCommand(os.Args[1:]) {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := harnessruntime.ACPSignInCommand(ctx, os.Args[1:])
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		return
	}
	if err := home.RestoreOwner(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dyna:", err)
		os.Exit(2)
	}
	if filepath.Base(os.Args[0]) == "gh" && os.Getenv(ghclient.ReadOnlyEnv) == "1" {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		if err := ghclient.SessionCommand(ctx, os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "gh:", err)
			os.Exit(1)
		}
		return
	}
	if !home.KeepsPrivateRoots(os.Args[1:]) {
		if err := store.CheckDataDir(); err != nil {
			fmt.Fprintln(os.Stderr, "dyna:", err)
			os.Exit(2)
		}
	}
	dropInheritedJournalPin(os.Args[1:])
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(versioncmd.CommandExitCode(err))
	}
}

// dropInheritedJournalPin clears DYNA_AGENT_JOURNAL_ROOT for every command but
// `dyna journal`. The pin confines a worker's own journal writes to its run or
// session; a dyna process that a worker starts (a nested run, a child subagent)
// owns different journals and must not inherit it.
func dropInheritedJournalPin(args []string) {
	if len(args) > 0 && args[0] == "journal" {
		return
	}
	_ = os.Unsetenv(runstore.AgentJournalRootEnv)
}

func newRootCommand() *cobra.Command {
	resolvedVersion := versioncmd.Resolve(version)
	// The engine and the CLI send their build on every call.
	callspec.Build = resolvedVersion
	root := &cobra.Command{
		Use:     "dyna",
		Short:   "Dynamic multi-agent workflows for any coding agent",
		Version: resolvedVersion,
		Long: "dyna runs JavaScript workflow scripts that orchestrate registered worker\n" +
			"profiles (claude-code, codex, antigravity, opencode, muse, grok, pi, custom CLIs). Agents use the CLI;\n" +
			"humans use the Dyna desktop app (desktop/) to run rooms, watch runs, and edit settings.",
		SilenceUsage: true,
	}
	root.SetVersionTemplate("dyna {{.Version}}\n")
	root.AddCommand(
		newEngineCommand(),
		managed.NewInstallCommand(),
		managed.NewUninstallCommand(),
		ask.NewCommand(),
		call.NewCommand(),
		cligit.NewCommand(),
		goal.NewCommand(),
		rules.NewCommand(),
		cliroot.NewCommand(),
		usage.NewCommand(),
		clidoctor.NewCommand(),
		cliroom.RoomCmd(),
		cliroom.PostCmd(),
		cliroom.VisualCmd(),
		cliroom.AttachmentCmd(),
		clisettings.NewCommand(),
		profiles.NewCommand(),
		workflows.NewRunCommand(),
		workflows.NewRunWorkerCommand(),
		workflows.NewTemplatesCommand(),
		workflows.NewRunsCommand(),
		journal.NewCommand(),
		climaintenance.NewCommand(),
		clisubagent.NewCommand(),
		shell.NewCommand(),
		computer.NewCommand(),
		computer.NewBrowserCommand(),
		computer.NewDaemonCommand(),
		hook.NewCommand(),
		clilibrary.NewCommand(),
		interactive.NewGuideCommand(),
		workflows.NewDemoCommand(),
		skillCmd(),
		mcpCmd(),
		cliruntime.NewCommand(),
		cliupdate.NewCommand(version),
		versioncmd.NewCommand(version),
	)
	appendAuthorityHelp(root)
	return root
}

func appendAuthorityHelp(cmd *cobra.Command) {
	if who := authority.Who(strings.TrimPrefix(cmd.CommandPath(), "dyna ")); who != "" {
		text := cmd.Long
		if text == "" {
			text = cmd.Short
		}
		cmd.Long = text + "\n\n" + who
	}
	for _, child := range cmd.Commands() {
		appendAuthorityHelp(child)
	}
}

func newEngineCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "engine", Short: "Run the Dyna engine and manage access"}
	cmd.AddCommand(&cobra.Command{Use: "run", Short: "Serve the Dyna web app", DisableFlagParsing: true, RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if code := engine.Main(ctx, args); code != 0 {
			return fmt.Errorf("engine exited with code %d", code)
		}
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "Show engine health: write latency and queue, event streams, SQLite contention", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(c.Context(), engine.StatusTimeout)
		defer cancel()
		return engine.Status(ctx, engineclient.New("").Call, c.OutOrStdout(), time.Now())
	}})
	var units string
	var writeOnly, restartComputers bool
	services := &cobra.Command{Use: "install-services", Short: "Install engine and computer user services", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		o := userservices.Options{Binary: exe, Dir: units, RestartComputers: restartComputers, Quiet: computerrestart.QuietNow, Output: c.OutOrStdout(),
			Healthy: func(ctx context.Context) error { return engine.WaitHealthy(ctx, engineclient.New("").Call, nil) }}
		if writeOnly {
			return userservices.Write(o)
		}
		return userservices.Install(c.Context(), o)
	}}
	services.Flags().StringVar(&units, "unit-dir", "", "unit directory (defaults to the user XDG config directory)")
	services.Flags().BoolVar(&writeOnly, "write-only", false, "write unit files without enabling services or contacting systemd")
	services.Flags().BoolVar(&restartComputers, "restart-computers", false, "restart the computer service when its runtime changed")
	cmd.AddCommand(services)
	cmd.AddCommand(&cobra.Command{Use: "connect", Short: "Manage Resident Labs Connect", DisableFlagParsing: true, RunE: func(cmd *cobra.Command, args []string) error {
		if code := engine.ConnectMain(args); code != 0 {
			return fmt.Errorf("engine connect exited with code %d", code)
		}
		return nil
	}})
	return cmd
}
