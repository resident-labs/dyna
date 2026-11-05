package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// serviceCommand is dyna service (spec 5.18). It is the owner's.
func serviceCommand(getClient func() client) *cobra.Command {
	service := &cobra.Command{Use: "service", Short: "Long-running containers Dyna runs on a host"}
	show := func(out io.Writer, s contract.Service) {
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\tgeneration %d, running %d\n", s.Spec.Name, s.Host, s.State, s.Reason, s.Cause, s.Generation, s.Deployed)
	}
	var file, host, image, data, restart string
	var env, secrets, ports, health []string
	var cpus float64
	var memory, disk string
	set := &cobra.Command{Use: "set ROOM [--file F | --name N --image I ...]", Short: "Declare a service, or change the declaration of the one with that name", Long: "Declare a service from a JSON file of the declaration (--file) or from flags. Changing a service bumps its generation and redeploys it. A secret is declared by name here and given its value with `service secret set`.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var spec contract.ServiceSpec
		if file != "" {
			text, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(text, &spec); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
		} else {
			spec = contract.ServiceSpec{Name: contract.ServiceName(cmd.Flag("name").Value.String()), Image: image, Data: data, CPUs: cpus, Memory: contract.ServiceSize(memory), Disk: contract.ServiceSize(disk), Host: contract.HostName(host), Restart: contract.ServiceRestart(restart), Secrets: secrets, Health: health}
			for _, p := range ports {
				spec.Ports = append(spec.Ports, contract.ServicePort(p))
			}
			if len(env) > 0 {
				spec.Env = map[string]string{}
			}
			for _, e := range env {
				name, value, _ := strings.Cut(e, "=")
				spec.Env[name] = value
			}
		}
		result, err := getClient().ServiceSet(cmd.Context(), contract.ServiceSetRequest{Room: contract.RoomID(args[0]), Spec: spec})
		if err == nil {
			show(cmd.OutOrStdout(), result.Service)
		}
		return err
	}}
	set.Flags().StringVar(&file, "file", "", "a JSON file holding the declaration")
	set.Flags().String("name", "", "the service's name")
	set.Flags().StringVar(&image, "image", "", "a room image, or a registry reference pinned by digest")
	set.Flags().StringVar(&host, "host", "", "the host, among the room's; admission picks one without it")
	set.Flags().StringVar(&data, "data", "", "where the data volume is mounted")
	set.Flags().StringVar(&restart, "restart", "", "always or on-failure")
	set.Flags().StringVar(&memory, "memory", "", "for example 512MiB")
	set.Flags().StringVar(&disk, "disk", "", "for example 1GiB")
	set.Flags().Float64Var(&cpus, "cpus", 0, "0.25 to 8")
	set.Flags().StringArrayVar(&ports, "port", nil, "PORT, PORT/udp or HOSTPORT:PORT/tcp")
	set.Flags().StringArrayVar(&env, "env", nil, "NAME=value; the value stays in shell history, so a secret goes through `service secret set`")
	set.Flags().StringArrayVar(&secrets, "secret", nil, "the name of a secret environment variable")
	set.Flags().StringArrayVar(&health, "health", nil, "the health command's words")
	list := &cobra.Command{Use: "list [ROOM]", Short: "List services", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.ServiceListRequest{}
		if len(args) == 1 {
			req.Room = contract.RoomID(args[0])
		}
		result, err := getClient().ServiceList(cmd.Context(), req)
		for _, s := range result.Services {
			show(cmd.OutOrStdout(), s)
		}
		return err
	}}
	detail := &cobra.Command{Use: "show ROOM NAME", Short: "Show a service and whether each secret is set", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().ServiceShow(cmd.Context(), contract.ServiceRef{Room: contract.RoomID(args[0]), Name: contract.ServiceName(args[1])})
		if err != nil {
			return err
		}
		show(cmd.OutOrStdout(), result.Service)
		for _, s := range result.Service.Secrets {
			state := "missing"
			if s.Set {
				state = "set"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secret %s\t%s\n", s.Name, state)
		}
		if result.Service.Detail != "" {
			fmt.Fprintln(cmd.OutOrStdout(), result.Service.Detail)
		}
		return nil
	}}
	var lines int
	var since time.Duration
	logs := &cobra.Command{Use: "logs ROOM NAME", Short: "Read a service's journal", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().ServiceLogs(cmd.Context(), contract.ServiceLogsRequest{Room: contract.RoomID(args[0]), Name: contract.ServiceName(args[1]), Lines: lines, Since: contract.DurationOf(since)})
		fmt.Fprint(cmd.OutOrStdout(), result.Text)
		return err
	}}
	logs.Flags().IntVar(&lines, "lines", 0, "how many lines, 1 to 2000 (default 200)")
	logs.Flags().DurationVar(&since, "since", 0, "only lines newer than this")
	action := func(use, short string, run func(client, *cobra.Command, contract.ServiceRef) (contract.ServiceActionResponse, error)) *cobra.Command {
		return &cobra.Command{Use: use + " ROOM NAME", Short: short, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			result, err := run(getClient(), cmd, contract.ServiceRef{Room: contract.RoomID(args[0]), Name: contract.ServiceName(args[1])})
			if err == nil {
				show(cmd.OutOrStdout(), result.Service)
			}
			return err
		}}
	}
	secret := &cobra.Command{Use: "secret", Short: "A service's secrets"}
	secretSet := &cobra.Command{Use: "set ROOM NAME SECRET", Short: "Set a secret's value, read from stdin; nothing reads it back", Long: "The value is read from stdin and is not hidden while typed on a terminal. Pipe it in to keep it off the screen.", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		value, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return err
		}
		_, err = getClient().ServiceSecretSet(cmd.Context(), contract.ServiceSecretSetRequest{Room: contract.RoomID(args[0]), Name: contract.ServiceName(args[1]), Secret: args[2], Value: strings.TrimSuffix(string(value), "\n")})
		return err
	}}
	secret.AddCommand(secretSet)
	service.AddCommand(set, list, detail, logs,
		action("restart", "Restart a service's unit", func(c client, cmd *cobra.Command, r contract.ServiceRef) (contract.ServiceActionResponse, error) {
			return c.ServiceRestart(cmd.Context(), r)
		}),
		action("stop", "Stop a service; it keeps its reservation", func(c client, cmd *cobra.Command, r contract.ServiceRef) (contract.ServiceActionResponse, error) {
			return c.ServiceStop(cmd.Context(), r)
		}),
		action("start", "Start a stopped service", func(c client, cmd *cobra.Command, r contract.ServiceRef) (contract.ServiceActionResponse, error) {
			return c.ServiceStart(cmd.Context(), r)
		}),
		action("update", "Redeploy the declared image", func(c client, cmd *cobra.Command, r contract.ServiceRef) (contract.ServiceActionResponse, error) {
			return c.ServiceUpdate(cmd.Context(), r)
		}),
		action("rollback", "Run the previous image again", func(c client, cmd *cobra.Command, r contract.ServiceRef) (contract.ServiceActionResponse, error) {
			return c.ServiceRollback(cmd.Context(), r)
		}),
		action("remove", "Remove a service, its data volume and its secrets", func(c client, cmd *cobra.Command, r contract.ServiceRef) (contract.ServiceActionResponse, error) {
			return c.ServiceRemove(cmd.Context(), r)
		}),
		secret)
	return service
}
