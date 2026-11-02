package main

import (
	"fmt"
	"os"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// poolCommand is dyna pool. The script travels in the request, because the engine cannot read this machine.
func poolCommand(getClient func() client) *cobra.Command {
	pool := &cobra.Command{Use: "pool", Short: "Node pools: scripts that get machines when a room needs them"}
	var script string
	var perSession bool
	var limit int
	var idle time.Duration
	add := &cobra.Command{Use: "add NAME", Short: "Add a pool; a session's request waits for the owner", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		text, err := os.ReadFile(script)
		if err != nil {
			return err
		}
		result, err := getClient().PoolAdd(cmd.Context(), contract.PoolAddRequest{Name: args[0], Script: string(text), Max: limit, PerSession: perSession, Idle: contract.DurationMS(idle.Milliseconds())})
		if err == nil && result.Operation != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "waiting for the owner:", result.Operation)
		}
		return err
	}}
	add.Flags().StringVar(&script, "script", "", "the script, which answers create, destroy and list")
	add.Flags().IntVar(&limit, "max", 0, "the most nodes it may hold")
	add.Flags().BoolVar(&perSession, "per-session", false, "give each box its own node; without this, sessions share nodes")
	add.Flags().DurationVar(&idle, "idle", 30*time.Minute, "how long a shared node may stay empty")
	_ = add.MarkFlagRequired("script") // fails only for an undefined flag
	_ = add.MarkFlagRequired("max")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := getClient().PoolList(cmd.Context(), contract.PoolListRequest{})
		if err != nil {
			return err
		}
		for _, p := range result.Pools {
			shape := "per session"
			if !p.PerSession {
				shape = "shared, idle " + (time.Duration(p.Idle) * time.Millisecond).String()
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%d of %d nodes\t%s\t%s\n", p.Name, p.Nodes, p.Max, shape, p.Problem)
		}
		return nil
	}}
	remove := &cobra.Command{Use: "remove NAME", Short: "Remove an empty pool that no room names", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().PoolRemove(cmd.Context(), contract.PoolRemoveRequest{Name: args[0]})
		return err
	}}
	pool.AddCommand(add, list, remove)
	return pool
}

// roomHostsCommand is dyna room hosts.
func roomHostsCommand(getClient func() client) *cobra.Command {
	var only []string
	var clear bool
	cmd := &cobra.Command{Use: "hosts ROOM", Short: "Restrict where a room's new boxes go", Long: "Keep a room's new boxes on these hosts and at most one pool, or with --clear let them use any host. Boxes already placed stay where they are. A session's request waits for the owner.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if clear == (len(only) > 0) {
			return fmt.Errorf("pass --only with the hosts and pool, or --clear")
		}
		result, err := getClient().RoomHosts(cmd.Context(), contract.RoomHostsRequest{Room: contract.RoomID(args[0]), Only: only})
		if err == nil && result.Operation != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "waiting for the owner:", result.Operation)
		}
		return err
	}}
	cmd.Flags().StringSliceVar(&only, "only", nil, "the pool and hosts this room may use (POOL[,HOST...])")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the rule")
	return cmd
}
