package ci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"text/tabwriter"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/websvc"
	"github.com/spf13/cobra"
)

// saveMachine sends one machine change to the engine, the one writer of the
// registry, so this alias obeys the same authority and rules as dyna machine.
var saveMachine = func(ctx context.Context, ref string, patch websvc.MachinePatch) (websvc.MachineDetail, error) {
	var detail websvc.MachineDetail
	err := engineclient.New("").Call(ctx, "SaveMachine", []any{ref, patch}, &detail)
	return detail, err
}

func registryCommands() []*cobra.Command {
	s := localci.Store{}
	return []*cobra.Command{nodeCommand(s), modeCommand(s, func() (string, error) {
		rec, err := watch.New().Notify.Rooms.ResolveContext("", true)
		return rec.ID, err
	})}
}

// nodeCommand is the old CI node command. Each subcommand forwards to the
// machines registry, where a CI node is a machine with the ci role.
//
// TODO: legacy/shim remove later — dyna git ci node forwards to dyna machine
func nodeCommand(s localci.Store) *cobra.Command {
	root := &cobra.Command{Use: "node", Short: "Manage shared local CI machines (use dyna machine)", Args: cobra.NoArgs}
	var ssh string
	var slots int
	add := &cobra.Command{
		Use: "add <name>", Short: "Add or update an enabled CI machine", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			store := machines.Store{DataDir: s.DataDir}
			m, err := store.Get(c.Context(), args[0])
			create := false
			if errors.Is(err, os.ErrNotExist) {
				if ssh == "" {
					local, err := store.GetLocal(c.Context())
					if err != nil {
						return err
					}
					return fmt.Errorf("CI machine %q needs --ssh user@host; this machine is the local machine %q, so add its CI slots with: dyna machine edit %s --ci-slots %d", args[0], local.Name, local.Name, slots)
				}
				create = true
				m = machines.Machine{Name: args[0]}
			} else if err != nil {
				return err
			}
			roles := m.Roles
			if !slices.Contains(roles, machines.RoleCI) {
				roles = append(roles, machines.RoleCI)
			}
			enabled := true
			patch := websvc.MachinePatch{Create: create, Enabled: &enabled, Roles: &roles, CISlots: &slots}
			if ssh != "" {
				patch.SSH = &ssh
			}
			saved, err := saveMachine(c.Context(), args[0], patch)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved CI machine %s with %d slots.\n", saved.Name, saved.CISlots)
			return nil
		},
	}
	add.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host (default: this machine)")
	add.Flags().IntVar(&slots, "slots", 1, "maximum concurrent CI checks")
	var asJSON bool
	list := &cobra.Command{
		Use: "list", Short: "List enabled and disabled CI machines", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			nodes, err := s.Nodes(c.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), nodes)
			}
			out := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(out, "NAME\tSSH\tSLOTS\tENABLED")
			for _, n := range nodes {
				ssh := n.SSH
				if ssh == "" {
					ssh = "local"
				}
				fmt.Fprintf(out, "%s\t%s\t%d\t%t\n", n.Name, ssh, n.Slots, n.Enabled)
			}
			return out.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "print machines as JSON")
	remove := &cobra.Command{
		Use: "rm <name>", Short: "Remove a CI machine, or only its ci role", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return (machines.Store{DataDir: s.DataDir}).RemoveRole(c.Context(), args[0], machines.RoleCI)
		},
	}
	disable := &cobra.Command{
		Use: "disable <name>", Short: "Stop scheduling new runs on a CI machine", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			store := machines.Store{DataDir: s.DataDir}
			m, err := store.Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			if !m.HasRole(machines.RoleCI) {
				return fmt.Errorf("machine %q: %w", args[0], os.ErrNotExist)
			}
			return store.SetEnabled(c.Context(), args[0], false)
		},
	}
	root.AddCommand(add, list, remove, disable)
	return root
}

func modeCommand(s localci.Store, roomID func() (string, error)) *cobra.Command {
	return &cobra.Command{
		Use: "mode [github|local|both]", Short: "Show or set CI mode for the current room", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 0 {
				if err := localci.ValidateMode(args[0]); err != nil {
					return err
				}
			}
			id, err := roomID()
			if err != nil {
				return err
			}
			if len(args) != 0 {
				if err := s.SetMode(c.Context(), id, args[0]); err != nil {
					return err
				}
			}
			mode, err := s.Mode(c.Context(), id)
			if err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), mode)
			return nil
		},
	}
}
