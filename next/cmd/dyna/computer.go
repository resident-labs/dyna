package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/resident-labs/dyna/box/computer"
	"github.com/spf13/cobra"
)

func computerCommand(socket string) *cobra.Command {
	root := &cobra.Command{Use: "computer"}
	var size string
	var full bool
	run := func(action computer.Action) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/computer", nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("computer start: %s", resp.Status)
			}
			var start struct {
				URL  string `json:"url"`
				Exit string `json:"exit"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
				return err
			}
			if start.Exit != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), start.Exit)
			}
			if action == "start" {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), start.URL)
				return err
			}
			d, err := computer.Connect(ctx, start.URL)
			if err != nil {
				return err
			}
			defer d.Close()
			return computer.Execute(ctx, d, action, args, size, full, cmd.OutOrStdout())
		}
	}
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: run("start")}
	open := &cobra.Command{Use: "open URL", Args: cobra.ExactArgs(1), RunE: run(computer.Open)}
	open.Flags().StringVar(&size, "size", "", "Viewport WIDTHxHEIGHT")
	click := &cobra.Command{Use: "click CSS", Args: cobra.ExactArgs(1), RunE: run(computer.Click)}
	typ := &cobra.Command{Use: "type CSS TEXT", Args: cobra.ExactArgs(2), RunE: run(computer.Type)}
	text := &cobra.Command{Use: "text [CSS]", Args: cobra.MaximumNArgs(1), RunE: run(computer.Text)}
	shot := &cobra.Command{Use: "screenshot FILE", Args: cobra.ExactArgs(1), RunE: run(computer.Screenshot)}
	shot.Flags().BoolVar(&full, "full", false, "Capture the full page")
	root.AddCommand(start, open, click, typ, text, shot)
	return root
}
