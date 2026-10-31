package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	dynaruntime "github.com/resident-labs/dyna/engine/runtime"
	"github.com/spf13/cobra"
)

func addEngine(root *cobra.Command) {
	var data, runtimeDir, hostData, profiles, webListen, backupDest, backupRecipient, gitName, gitEmail string
	var webRoutes []string
	var maxDepth int
	var approvalExpiry time.Duration
	home, _ := os.UserConfigDir()
	cmd := &cobra.Command{Use: "engine", Long: "Run the engine. From the repository root, build both required files:\n  (cd next && go build -o \"$HOME/dyna-m1/bin/dyna\" ./cmd/dyna)\n  next/third_party/pi/scripts/pi-pack.sh \"$HOME/dyna-m1/bin/pi.tgz\"\nCreate the output directory first. Keep pi.tgz beside dyna.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		for _, path := range []string{data, runtimeDir, hostData, profiles} {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("engine paths must be absolute")
			}
		}
		if maxDepth < 0 {
			return fmt.Errorf("session.max-depth must be non-negative")
		}
		if approvalExpiry <= 0 {
			return fmt.Errorf("approvals.expiry must be positive")
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return dynaruntime.Run(ctx, dynaruntime.Config{Data: data, Runtime: runtimeDir, HostData: hostData, Profiles: profiles, Version: version, ApprovalsExpiry: approvalExpiry, SessionMaxDepth: maxDepth, WebListen: webListen, WebRoutes: webRoutes, BackupDest: backupDest, BackupRecipient: backupRecipient, GitName: gitName, GitEmail: gitEmail})
	}}
	cmd.Flags().StringVar(&gitName, "git.name", "", "Git commit identity name for boxes")
	cmd.Flags().StringVar(&gitEmail, "git.email", "", "Git commit identity email for boxes")
	cmd.Flags().IntVar(&maxDepth, "session.max-depth", 3, "Maximum session depth (0 disables the limit)")
	cmd.Flags().StringVar(&backupDest, "backup.dest", "", "SSH destination for encrypted backups")
	cmd.Flags().StringVar(&backupRecipient, "backup.recipient", "", "age public recipient for encrypted backups")
	cmd.Flags().StringVar(&webListen, "web.listen", "", "Web listener address (127.0.0.1 only)")
	cmd.Flags().StringArrayVar(&webRoutes, "web.route", nil, "Allowed web origin (repeatable)")
	cmd.Flags().StringVar(&data, "data", filepath.Join(home, "dyna-next"), "Engine data directory")
	cmd.Flags().StringVar(&runtimeDir, "runtime", filepath.Dir(defaultSocket()), "Engine runtime directory")
	cmd.Flags().StringVar(&hostData, "host-data", "", "Absolute host data directory")
	cmd.Flags().DurationVar(&approvalExpiry, "approvals.expiry", 24*time.Hour, "Pending operation expiry")
	cmd.Flags().StringVar(&profiles, "profiles", filepath.Join(home, "dyna-next", "profiles.json"), "Profiles configuration")
	root.AddCommand(cmd)
}
