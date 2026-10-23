// Package machine exposes the machines registry as `dyna machine`: the engine
// host, the machines that run agents, and the machines that run local CI.
package machine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

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
			"slot. `dyna git ci node` still works and forwards to these commands.\n\n" +
			"`dyna machine probe` reads a machine's capabilities and load over SSH. The\n" +
			"engine also probes every enabled machine when it starts, when a machine\n" +
			"changes, and every five minutes.",
		Args: cobra.NoArgs,
	}
	root.AddCommand(listCommand(), showCommand(), addCommand(), editCommand(), probeCommand(), removeCommand())
	return root
}

func store() machines.Store { return machines.Store{} }

// prober is the prober the CLI saves through.
func prober() machines.Prober { return machines.Prober{Store: store()} }

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
	fmt.Fprintln(out, "NAME\tROLES\tSTATUS\tSSH\tSLOTS\tENABLED\tLOCAL")
	for _, m := range list {
		ssh, local := m.SSH, ""
		if ssh == "" {
			ssh = "local"
		}
		if m.Local {
			local = "*"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%d\t%t\t%s\n", m.Name, m.RoleNames(), m.Status(), ssh, m.CISlots, m.Enabled, local)
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
			fmt.Fprintf(out, "Status:   %s\n", m.Status())
			fmt.Fprintf(out, "Probed:   %s\n", stamp(m.LastProbe))
			fmt.Fprintf(out, "Seen:     %s\n", stamp(m.LastSeen))
			if m.ProbeError != "" {
				fmt.Fprintf(out, "Error:    %s\n", m.ProbeError)
			}
			writeCapabilities(out, m)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the machine as JSON")
	return cmd
}

// writeCapabilities prints what the last probe read, or says that no probe
// has reached the machine.
func writeCapabilities(out io.Writer, m machines.Machine) {
	caps, load := m.Capabilities, m.Load
	if m.LastSeen.IsZero() {
		fmt.Fprintln(out, "System:   not probed yet")
		return
	}
	system := caps.OS
	if caps.Arch != "" {
		system += "/" + caps.Arch
	}
	fmt.Fprintf(out, "System:   %s, %d CPUs, %s memory, %s available\n",
		system, caps.CPUs, size(caps.MemTotal), size(caps.MemAvailable))
	fmt.Fprintf(out, "GPU:      %t\n", caps.GPU)
	fmt.Fprintf(out, "Runtime:  %s\n", joinOr(caps.Containers.Names(), "none"))
	fmt.Fprintf(out, "Computers: %t, stream: %t\n", caps.Computers.Ready(), caps.Computers.Stream)
	fmt.Fprintf(out, "Dyna:     %s\n", orNone(caps.DynaVersion))
	if load.CPUPercent != nil {
		fmt.Fprintf(out, "Load:     %.2f %.2f %.2f, CPU %.0f%%, %s free\n",
			load.LoadAvg[0], load.LoadAvg[1], load.LoadAvg[2], *load.CPUPercent, size(load.MemAvailable))
		return
	}
	fmt.Fprintf(out, "Load:     %.2f %.2f %.2f, %s free\n",
		load.LoadAvg[0], load.LoadAvg[1], load.LoadAvg[2], size(load.MemAvailable))
}

func probeCommand() *cobra.Command {
	var asJSON, all bool
	cmd := &cobra.Command{
		Use:   "probe [<name>|--all]",
		Short: "Read a machine's capabilities and load, and store them",
		Long: "Probe one machine, or every enabled machine with --all, and store what\n" +
			"the probe read. A remote machine is read over SSH with no credential. A\n" +
			"failed probe keeps the last capabilities it read, and marks the machine\n" +
			"offline. A disabled machine is not probed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return errors.New("probe: give a machine name or --all, not both")
			}
			if !all && len(args) == 0 {
				return errors.New("probe: give a machine name or --all")
			}
			targets := args
			if all {
				list, err := store().List(c.Context())
				if err != nil {
					return err
				}
				targets = nil
				for _, m := range list {
					if m.Enabled {
						targets = append(targets, m.Name)
					}
				}
			}
			p, out := prober(), c.OutOrStdout()
			var failures []error
			for _, name := range targets {
				m, err := store().Get(c.Context(), name)
				if err != nil {
					failures = append(failures, err)
					continue
				}
				started := time.Now()
				stored, err := p.ProbeAndSave(c.Context(), m)
				if errors.Is(err, machines.ErrDisabled) {
					failures = append(failures, fmt.Errorf("%s is disabled; enable it with dyna machine edit %s --enable", name, name))
					continue
				}
				if err != nil {
					failures = append(failures, err)
					continue
				}
				if !asJSON {
					writeProbe(out, stored, time.Since(started))
				}
			}
			if asJSON {
				list, err := store().List(c.Context())
				if err != nil {
					return err
				}
				if err := jsonout.Write(out, list); err != nil {
					return err
				}
			}
			return errors.Join(failures...)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "probe every enabled machine")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the probed machines as JSON")
	return cmd
}

// writeProbe prints one probe result. A machine that could not be reached
// prints as a result with its error, not as a command failure.
func writeProbe(out io.Writer, m machines.Machine, took time.Duration) {
	fmt.Fprintf(out, "%s: %s in %s\n", m.Name, m.Status(), took.Round(time.Millisecond))
	if m.ProbeError != "" {
		fmt.Fprintf(out, "  Error: %s\n", m.ProbeError)
		return
	}
	writeCapabilities(out, m)
}

func addCommand() *cobra.Command {
	var ssh string
	var roleNames []string
	var slots int
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a machine, or update the machine with this name",
		Long: "Add a machine. Its name must be new: use edit to change a stored machine.\n" +
			"The default role is ci with one slot. A positive --ci-slots adds the ci\n" +
			"role.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s := store()
			switch _, err := s.Get(c.Context(), args[0]); {
			case err == nil:
				return fmt.Errorf("machine %s exists; use dyna machine edit", args[0])
			case !errors.Is(err, os.ErrNotExist):
				return err
			}
			roles, err := parseRoles(roleNames)
			if err != nil {
				return err
			}
			if len(roles) == 0 {
				roles = []machines.Role{machines.RoleCI}
			}
			if c.Flags().Changed("ci-slots") && slots > 0 && !slices.Contains(roles, machines.RoleCI) {
				roles = append(roles, machines.RoleCI)
			}
			m := machines.Machine{Name: args[0], Roles: roles, SSH: ssh, CISlots: slots, Enabled: true}
			saved, err := s.Save(c.Context(), m)
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
				return fmt.Errorf("edit %s: give at least one of --rename, --ssh, --role, --ci-slots, --enable or --disable", args[0])
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

// stamp prints a probe time, or a dash before the first probe.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.DateTime)
}

// size prints a byte count in the largest unit that keeps it readable.
func size(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value, exp := float64(bytes), 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGTP"[exp-1])
}

// joinOr joins names for one line, or prints the fallback when there is none.
func joinOr(names []string, fallback string) string {
	if len(names) == 0 {
		return fallback
	}
	return strings.Join(names, ",")
}

// orNone prints a value, or a dash when the probe read none.
func orNone(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
