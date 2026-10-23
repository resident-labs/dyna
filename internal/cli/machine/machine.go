// Package machine exposes the machines registry as `dyna machine`: the engine
// host, the machines that run agents, and the machines that run local CI.
package machine

import (
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"dyna-agent/internal/cli/jsonout"
	"dyna-agent/internal/machines"
)

// NewCommand builds the `dyna machine` group.
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "machine",
		Short: "List and change the machines that run CI and agents",
		Long: "The registry holds this machine, marked local, and each machine that Dyna\n" +
			"can use. A machine has one or more roles: engine (this machine only),\n" +
			"agents, and ci. A ci machine needs an SSH destination and at least one CI\n" +
			"slot. `dyna git ci node` still works and forwards to these commands.",
		Args: cobra.NoArgs,
	}
	root.AddCommand(listCommand(), showCommand(), addCommand(), editCommand(), removeCommand())
	return root
}

func store() machines.Store { return machines.Store{} }

func listCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List machines, this machine first",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			list, err := store().List(c.Context())
			if err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), list)
			}
			return writeTable(c.OutOrStdout(), list)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machines as JSON")
	return cmd
}

// writeTable prints one row per machine. The LOCAL column marks the machine
// that runs this engine.
func writeTable(w io.Writer, list []machines.Machine) error {
	out := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(out, "NAME\tROLES\tSSH\tSLOTS\tENABLED\tLOCAL")
	for _, m := range list {
		ssh, local := m.SSH, ""
		if ssh == "" {
			ssh = "local"
		}
		if m.Local {
			local = "*"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%d\t%t\t%s\n", m.Name, m.RoleNames(), ssh, m.CISlots, m.Enabled, local)
	}
	return out.Flush()
}

func showCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one machine",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			m, err := store().Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return jsonout.Write(c.OutOrStdout(), m)
			}
			out := c.OutOrStdout()
			ssh := m.SSH
			if ssh == "" {
				ssh = "local"
			}
			fmt.Fprintf(out, "Name:     %s\n", m.Name)
			fmt.Fprintf(out, "ID:       %s\n", m.ID)
			fmt.Fprintf(out, "Roles:    %s\n", m.RoleNames())
			fmt.Fprintf(out, "SSH:      %s\n", ssh)
			fmt.Fprintf(out, "CI slots: %d\n", m.CISlots)
			fmt.Fprintf(out, "Enabled:  %t\n", m.Enabled)
			fmt.Fprintf(out, "Local:    %t\n", m.Local)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the machine as JSON")
	return cmd
}

func addCommand() *cobra.Command {
	var ssh string
	var roleNames []string
	var slots int
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a machine, or update the machine with this name",
		Long: "Add a machine, or replace the settings of the stored machine with this\n" +
			"name. The default role is ci with one slot. A positive --ci-slots adds\n" +
			"the ci role.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			roles, err := parseRoles(roleNames)
			if err != nil {
				return err
			}
			if len(roles) == 0 {
				roles = []machines.Role{machines.RoleCI}
			}
			if c.Flags().Changed("ci-slots") && slots > 0 && !hasRole(roles, machines.RoleCI) {
				roles = append(roles, machines.RoleCI)
			}
			m := machines.Machine{Name: args[0], Roles: roles, SSH: ssh, CISlots: slots, Enabled: true}
			saved, err := store().Save(c.Context(), m)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved machine %s with roles %s.\n", saved.Name, saved.RoleNames())
			return nil
		},
	}
	cmd.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host")
	cmd.Flags().StringArrayVar(&roleNames, "role", nil, "role to give the machine: ci or agents (repeatable; default ci)")
	cmd.Flags().IntVar(&slots, "ci-slots", 1, "maximum concurrent CI checks; a positive value adds the ci role")
	return cmd
}

func editCommand() *cobra.Command {
	var rename, ssh string
	var roleNames []string
	var slots int
	var enable, disable bool
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Change a machine",
		Long: "Change a machine. --role replaces every role, so give all of the roles the\n" +
			"machine should keep. A positive --ci-slots adds the ci role, and a machine\n" +
			"that gets the ci role without a slot count gets one slot.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s := store()
			m, err := s.Get(c.Context(), args[0])
			if err != nil {
				return err
			}
			changed := false
			if c.Flags().Changed("rename") {
				if err := machines.ValidateName(rename); err != nil {
					return err
				}
				m.Name, changed = rename, true
			}
			if c.Flags().Changed("ssh") {
				m.SSH, changed = ssh, true
			}
			if c.Flags().Changed("role") {
				if m.Roles, err = parseRoles(roleNames); err != nil {
					return err
				}
				changed = true
			}
			if c.Flags().Changed("ci-slots") {
				m.CISlots, changed = slots, true
				if slots > 0 && !m.HasRole(machines.RoleCI) {
					m.Roles = append(m.Roles, machines.RoleCI)
				}
			}
			if enable {
				m.Enabled, changed = true, true
			}
			if disable {
				m.Enabled, changed = false, true
			}
			if !changed {
				return errors.New("edit " + args[0] + ": give at least one of --rename, --ssh, --role, --ci-slots, --enable or --disable")
			}
			if m.HasRole(machines.RoleCI) && m.CISlots < 1 {
				m.CISlots = 1
			}
			saved, err := s.Save(c.Context(), m)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Saved machine %s with roles %s.\n", saved.Name, saved.RoleNames())
			return nil
		},
	}
	cmd.Flags().StringVar(&rename, "rename", "", "new name for the machine")
	cmd.Flags().StringVar(&ssh, "ssh", "", "SSH destination as host or user@host")
	cmd.Flags().StringArrayVar(&roleNames, "role", nil, "replace the roles with these: engine, agents or ci (repeatable)")
	cmd.Flags().IntVar(&slots, "ci-slots", 0, "maximum concurrent CI checks; a positive value adds the ci role")
	cmd.Flags().BoolVar(&enable, "enable", false, "allow work on the machine")
	cmd.Flags().BoolVar(&disable, "disable", false, "stop new work on the machine")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func removeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a machine; this machine cannot be removed",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := store().Remove(c.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Removed machine %s.\n", args[0])
			return nil
		},
	}
}

func parseRoles(names []string) ([]machines.Role, error) {
	roles := make([]machines.Role, 0, len(names))
	for _, name := range names {
		role, err := machines.ParseRole(name)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, nil
}

func hasRole(roles []machines.Role, role machines.Role) bool {
	for _, have := range roles {
		if have == role {
			return true
		}
	}
	return false
}
