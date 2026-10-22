// Package call registers explicit owner call requests.
package call

import (
	"encoding/json"
	"fmt"
	"os"

	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	var reason, roomRef string
	var jsonOut bool
	cmd := &cobra.Command{Use: "call", Short: "Root rings the owner with a reason", Args: cobra.NoArgs}
	cmd.PersistentFlags().StringVar(&roomRef, "room", "", "room name or ID (defaults to your room)")
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "print JSON")
	cmd.Flags().StringVar(&reason, "reason", "", "why the owner should join the call (required)")
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		args, err := callArguments(roomRef, reason)
		if err != nil {
			return err
		}
		var req callrequests.Request
		if err := engineClient().Call(c.Context(), "RequestCall", args, &req); err != nil {
			return fmt.Errorf("the call request was not sent: %w", err)
		}
		if jsonOut {
			return json.NewEncoder(c.OutOrStdout()).Encode(req)
		}
		_, err = fmt.Fprintf(c.OutOrStdout(), "call %s ringing\n", req.ID)
		return err
	}
	list := &cobra.Command{Use: "list", Short: "List call requests in this room", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, snd, err := caller()
		if err != nil {
			return err
		}
		rec, err := s.CallRoom(snd, roomRef)
		if err != nil {
			return err
		}
		requests, err := s.CallRequestStore().List(rec.ID)
		if err != nil {
			return err
		}
		if jsonOut {
			return json.NewEncoder(c.OutOrStdout()).Encode(requests)
		}
		for _, req := range requests {
			if _, err := fmt.Fprintf(c.OutOrStdout(), "%s %s: %s\n", req.ID, req.State, req.Reason); err != nil {
				return err
			}
		}
		return nil
	}}
	withdraw := &cobra.Command{Use: "withdraw <id>", Short: "Withdraw a call request", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		callArgs, err := callArguments(roomRef, args[0])
		if err != nil {
			return err
		}
		if err := engineClient().Call(c.Context(), "WithdrawCall", callArgs, nil); err != nil {
			return fmt.Errorf("the call request was not withdrawn: %w", err)
		}
		return nil
	}}
	cmd.AddCommand(list, withdraw)
	return cmd
}

func caller() (*notify.Service, notify.Sender, error) {
	s := notify.Default()
	s.Sessions.Executable, _ = os.Executable()
	snd, err := s.SelfSender()
	return s, snd, err
}

// engineClient permits isolated tests to use the normal restart failure path.
var engineClient = func() *engineclient.Client { return engineclient.New("") }

// callArguments carries room selection and working directory, never identity.
// Only the engine may create the working directory's default room.
func callArguments(ref, value string) ([]any, error) {
	if ref == "" {
		stamp, _ := mode.StampFromEnv()
		ref = stamp.RoomID
		if ref == "" {
			ref = stamp.Room
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return []any{ref, dir, value}, nil
}
