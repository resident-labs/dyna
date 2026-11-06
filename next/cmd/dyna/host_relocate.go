package main

import (
	"fmt"
	"path/filepath"

	"github.com/resident-labs/dyna/engine/store"
	"github.com/spf13/cobra"
)

func relocateEngineCommand() *cobra.Command {
	var data, from, fromSSH, to string
	cmd := &cobra.Command{
		Use:   "relocate-engine --data DIR --from OLD --from-ssh user@REMOTE --to NEW",
		Short: "Relocate a stopped engine's existing store (owner offline only)",
		Long: `Relocate a stopped engine's existing store, preserving physical host IDs and all history.
--from-ssh is the former engine's now-remote SSH endpoint, not the destination's.
The destination keeps its registered SSH endpoint. Names are not resolved or probed.
This command requires the same schema version as the store; it never initializes or migrates it.
It takes the engine's exclusive data-folder lock. Stop BOTH engines and automatic restarters
before the final consistent copy: locks cannot exclude writers on independent copies.
Pause the former engine's rooms, refresh its running sessions, and wait until their stored
box rows are STOPPED before the final copy. Active former-engine boxes and subscription Runs
refuse relocation. Finish or explicitly stop unfinished control-plane host-exec runs and operations first.
Only the former host's SSH endpoint and the two derived engine-host flags change.
CI/agents switches, GPU facts, room rules, grants, boxes, jobs, settings and secrets stay.
This does not move volumes or repair existing direct shared-folder binds. With the engine
stopped and box rows already STOPPED, the owner may remove only those stopped containers,
NEVER their named volumes. After resume the engine recreates missing stopped containers
with the new shared mount. Refresh alone does not replace the mount; session restart can
discard volumes and is not a relocation procedure. No containers are removed by this command.
Repeating the command is safe. For rollback, copy the latest stopped data back, reverse
--from and --to, and supply the other former engine's remote --from-ssh endpoint.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !filepath.IsAbs(data) {
				return fmt.Errorf("--data must be an absolute engine data directory")
			}
			move, err := store.NewEngineRelocation(from, fromSSH, to)
			if err != nil {
				return err
			}
			if err := store.RelocateEngine(cmd.Context(), data, move); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Engine relocated from %s to %s; %s SSH is %s. No boxes, volumes or jobs moved. Stopped former-engine containers still need volume-preserving recreation before shared writes resume.\n", from, to, from, fromSSH)
			return err
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "Absolute existing engine data directory (required)")
	cmd.Flags().StringVar(&from, "from", "", "Registered former engine host (required)")
	cmd.Flags().StringVar(&fromSSH, "from-ssh", "", "Former engine's now-remote user@host endpoint, never localhost (required)")
	cmd.Flags().StringVar(&to, "to", "", "Registered new engine host; keeps its SSH endpoint (required)")
	for _, flag := range []string{"data", "from", "from-ssh", "to"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}
