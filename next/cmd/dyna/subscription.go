package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

// runFiles reads each BOXPATH=LOCALPATH into a RunFile.
func runFiles(specs []string) ([]contract.RunFile, error) {
	var files []contract.RunFile
	for _, spec := range specs {
		boxPath, local, ok := strings.Cut(spec, "=")
		if !ok {
			return nil, fmt.Errorf("--file %q is not BOXPATH=LOCALPATH", spec)
		}
		body, err := os.ReadFile(local)
		if err != nil {
			return nil, err
		}
		files = append(files, contract.RunFile{Path: boxPath, Body: string(body)})
	}
	return files, nil
}

func subscriptionScope(machine bool, room, session string) (contract.SubscriptionScope, error) {
	switch {
	case machine && room == "" && session == "":
		return contract.ScopeMachine, nil
	case !machine && session != "" && room == "":
		return contract.ScopeSession, nil
	case !machine && room != "" && session == "":
		return contract.ScopeRoom, nil
	}
	return "", fmt.Errorf("give exactly one of --machine, --room and --session")
}

func describeSubscription(info contract.SubscriptionInfo) string {
	sub := info.Subscription
	when := "every " + (time.Duration(sub.Every) * time.Millisecond).String()
	if sub.Every == 0 {
		names := make([]string, 0, len(sub.On))
		for _, kind := range sub.On {
			names = append(names, string(kind))
		}
		when = "on " + strings.Join(names, ",")
	}
	action := "wake"
	if sub.Run != nil {
		action = "run " + sub.Run.Image + ": " + strings.Join(sub.Run.Command, " ")
	} else if sub.Wake != nil {
		action = "wake " + string(sub.Wake.Session)
	}
	state := []string{}
	if info.Disabled {
		state = append(state, "disabled")
	}
	if info.Running {
		state = append(state, "running")
	}
	if info.Last != "" {
		state = append(state, "last "+string(info.Last))
	}
	if info.Failures > 0 {
		state = append(state, fmt.Sprintf("%d failures", info.Failures))
	}
	if info.NextAt != nil {
		state = append(state, "next "+info.NextAt.Local().Format(time.DateTime))
	}
	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", sub.ID, sub.Scope, when, action, strings.Join(state, ", "))
}

func subscriptionCommand(get func() *socketClient) *cobra.Command {
	root := &cobra.Command{Use: "subscription", Short: "Typed subscriptions: wake a session or run a small box on events or on a schedule"}
	var machine bool
	var room, session, image, size, wake string
	var on, files []string
	var every time.Duration
	add := &cobra.Command{Use: "add (--machine | --room ROOM | --session SESSION) (--on TYPE | --every DURATION) (--wake SESSION | [--image NAME] [--size SIZE] [--file BOXPATH=LOCALPATH] -- COMMAND [ARG...])", Short: "Add a subscription", RunE: func(cmd *cobra.Command, args []string) error {
		scope, err := subscriptionScope(machine, room, session)
		if err != nil {
			return err
		}
		req := contract.SubscriptionAddRequest{Scope: scope, Room: contract.RoomID(room), Session: contract.SessionID(session), Every: contract.DurationMS(every.Milliseconds())}
		for _, kind := range on {
			req.On = append(req.On, contract.EventType(kind))
		}
		if wake != "" {
			req.Wake = &contract.Principal{Kind: contract.PrincipalSession, Session: contract.SessionID(wake)}
		}
		if len(args) > 0 {
			run := contract.RunSpec{Command: args, Image: image, Size: contract.BoxSizeName(size)}
			if run.Files, err = runFiles(files); err != nil {
				return err
			}
			req.Run = &run
		}
		result, err := call[contract.SubscriptionAddRequest, contract.SubscriptionAddResponse](cmd.Context(), get(), contract.MethodSubscriptionAdd, req)
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.ID)
		}
		return err
	}}
	add.Flags().BoolVar(&machine, "machine", false, "Machine scope")
	add.Flags().StringVar(&room, "room", "", "Room scope")
	add.Flags().StringVar(&session, "session", "", "Session scope")
	add.Flags().StringArrayVar(&on, "on", nil, "Event type (repeatable)")
	add.Flags().DurationVar(&every, "every", 0, "Schedule, at least one minute")
	add.Flags().StringVar(&wake, "wake", "", "Session ID to wake")
	add.Flags().StringVar(&image, "image", "base", "Run image: base, or an image folder of the room's default repository")
	add.Flags().StringVar(&size, "size", string(contract.BoxSizeSmall), "Run box size: small or agent")
	add.Flags().StringArrayVar(&files, "file", nil, "Run file as BOXPATH=LOCALPATH (repeatable); the path is relative to the folder the command starts in")
	var listRoom string
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := call[contract.SubscriptionListRequest, contract.SubscriptionListResponse](cmd.Context(), get(), contract.MethodSubscriptionList, contract.SubscriptionListRequest{Room: contract.RoomID(listRoom)})
		if err != nil {
			return err
		}
		for _, info := range result.Subscriptions {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), describeSubscription(info)); err != nil {
				return err
			}
		}
		return nil
	}}
	list.Flags().StringVar(&listRoom, "room", "", "Room; with none the owner sees machine subscriptions")
	remove := &cobra.Command{Use: "remove ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := call[contract.SubscriptionRemoveRequest, contract.SubscriptionRemoveResponse](cmd.Context(), get(), contract.MethodSubscriptionRemove, contract.SubscriptionRemoveRequest{ID: contract.SubscriptionID(args[0])})
		return err
	}}
	root.AddCommand(add, list, remove)
	for _, enabled := range []bool{true, false} {
		name := "disable"
		if enabled {
			name = "enable"
		}
		root.AddCommand(&cobra.Command{Use: name + " ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			_, err := call[contract.SubscriptionSetEnabledRequest, contract.SubscriptionSetEnabledResponse](cmd.Context(), get(), contract.MethodSubscriptionSetEnabled, contract.SubscriptionSetEnabledRequest{ID: contract.SubscriptionID(args[0]), Enabled: enabled})
			return err
		}})
	}
	return root
}
