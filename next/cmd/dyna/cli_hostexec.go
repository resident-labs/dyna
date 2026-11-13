package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func hostexecCommands(getClient func() client) (*cobra.Command, *cobra.Command) {
	hostexec := &cobra.Command{Use: "hostexec"}
	var runHost, cwd string
	var timeout time.Duration
	run := &cobra.Command{Use: "bash --host HOST COMMAND", Short: "Run a command on a host with bash -c, under a host grant.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c := getClient()
		var id [16]byte
		_, _ = rand.Read(id[:])
		req := contract.HostexecRunRequest{Host: contract.HostName(runHost), Command: args[0], Cwd: cwd, Timeout: contract.DurationMS(timeout.Milliseconds()), ClientID: hex.EncodeToString(id[:])}
		// An interrupt stops the run and still waits for its result. A dropped call would not stop it, so the stop is asked
		// for by name, and a second interrupt ends this at once.
		interrupted, restore := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer restore()
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			select {
			case <-finished:
				return
			case <-interrupted.Done():
			}
			restore()
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 5*time.Second)
			defer cancel()
			if _, err := c.HostexecStop(stopCtx, contract.HostexecStopRequest{ClientID: req.ClientID}); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "The run could not be stopped:", err)
			}
		}()
		res, err := c.HostexecRun(cmd.Context(), req)
		if err != nil {
			return err
		}
		op := res.Operation
		if op.Result == nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Command %s waits for approval; op.ran wakes the session with the result.\n", op.ID)
			return err
		}
		if _, err = io.WriteString(cmd.OutOrStdout(), op.Result.Output); err != nil {
			return err
		}
		if op.Result.OutputFile != "" {
			if err = c.File(cmd.Context(), op.Result.OutputFile, cmd.OutOrStdout()); err != nil {
				return err
			}
		}
		if op.Result.Truncated {
			fmt.Fprintln(cmd.ErrOrStderr(), "Output truncated at 8 MiB.")
		}
		if op.Result.Error != "" {
			return fmt.Errorf("%s", op.Result.Error)
		}
		if op.Result.ExitCode != 0 {
			cmd.SilenceErrors = true
			return commandExit(op.Result.ExitCode)
		}
		return nil
	}}
	var grantHost, grantRoom, grantTo, grantReason string
	grant := &cobra.Command{Use: "grant --host HOST --reason WHY", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		args := contract.HostexecGrantArgs{Room: contract.RoomID(grantRoom), Hosts: []contract.HostName{contract.HostName(grantHost)}, Reason: grantReason}
		if grantTo != "" {
			args.To = contract.Principal{Kind: contract.PrincipalSession, Session: contract.SessionID(grantTo)}
		}
		r, err := getClient().OpSubmit(cmd.Context(), contract.OpSubmitRequest{Operation: contract.Operation{Kind: args.OpKind(), Room: args.Room, Args: args}})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Operation.ID)
		}
		return err
	}}
	grant.Flags().StringVar(&grantHost, "host", "", "Host name")
	grant.Flags().StringVar(&grantReason, "reason", "", "Why the session needs access")
	grant.Flags().StringVar(&grantTo, "to", "", "Session that gets the grant (owner only)")
	grant.Flags().StringVar(&grantRoom, "room", "", "Room ID (owner only)")
	_ = grant.MarkFlagRequired("host")   // fails only for an undefined flag
	_ = grant.MarkFlagRequired("reason") // fails only for an undefined flag
	run.Flags().StringVar(&runHost, "host", "", "Host name")
	run.Flags().StringVar(&cwd, "cwd", "", "Working directory")
	run.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "Run timeout")
	_ = run.MarkFlagRequired("host")
	hostexec.AddCommand(grant)
	return hostexec, run
}
