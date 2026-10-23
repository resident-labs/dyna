package ci

import (
	"fmt"
	"text/tabwriter"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/watch"
	"github.com/spf13/cobra"
)

func registryCommands() []*cobra.Command {
	s := localci.Store{}
	return []*cobra.Command{nodeCommand(s), modeCommand(s, func() (string, error) {
		rec, err := watch.New().Notify.Rooms.ResolveContext("", true)
		return rec.ID, err
	})}
}

func nodeCommand(s localci.Store) *cobra.Command {
	root := &cobra.Command{Use: "node", Short: "Manage shared local CI runners", Args: cobra.NoArgs}
	var ssh string
	var slots int
	add := &cobra.Command{
		Use: "add <name>", Short: "Add or update an enabled CI runner", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			n := localci.Node{Name: args[0], SSH: ssh, Slots: slots, Enabled: true}
			if err := s.PutNode(c.Context(), n); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved CI node %s with %d slots.\n", n.Name, n.Slots)
			return nil
		},
	}
	add.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host (default: this machine)")
	add.Flags().IntVar(&slots, "slots", 1, "maximum concurrent CI checks")
	var asJSON bool
	list := &cobra.Command{
		Use: "list", Short: "List enabled and disabled CI runners", Args: cobra.NoArgs,
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
	list.Flags().BoolVar(&asJSON, "json", false, "print nodes as JSON")
	remove := &cobra.Command{
		Use: "rm <name>", Short: "Remove a CI runner", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return s.RemoveNode(c.Context(), args[0]) },
	}
	disable := &cobra.Command{
		Use: "disable <name>", Short: "Stop scheduling new runs on a CI runner", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return s.DisableNode(c.Context(), args[0]) },
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
