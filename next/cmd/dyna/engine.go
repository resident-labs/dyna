package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	dynaruntime "github.com/resident-labs/dyna/engine/runtime"
	"github.com/spf13/cobra"
)

func addEngine(root *cobra.Command) {
	var data, runtimeDir, hostData, profiles string
	home, _ := os.UserConfigDir()
	cmd := &cobra.Command{Use: "engine", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		for _, path := range []string{data, runtimeDir, hostData, profiles} {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("engine paths must be absolute")
			}
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return dynaruntime.Run(ctx, dynaruntime.Config{Data: data, Runtime: runtimeDir, HostData: hostData, Profiles: profiles, Version: version})
	}}
	cmd.Flags().StringVar(&data, "data", filepath.Join(home, "dyna-next"), "Engine data directory")
	cmd.Flags().StringVar(&runtimeDir, "runtime", filepath.Dir(defaultSocket()), "Engine runtime directory")
	cmd.Flags().StringVar(&hostData, "host-data", "", "Absolute host data directory")
	cmd.Flags().StringVar(&profiles, "profiles", filepath.Join(home, "dyna-next", "profiles.json"), "Profiles configuration")
	root.AddCommand(cmd)
}
