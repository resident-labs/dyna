package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
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

// subscriptionScope reads the scope flags. --session with --room finds the session by its name in that room.
func subscriptionScope(machine bool, room, session string) (contract.SubscriptionScope, error) {
	switch {
	case machine && room == "" && session == "":
		return contract.ScopeMachine, nil
	case !machine && session != "":
		return contract.ScopeSession, nil
	case !machine && room != "":
		return contract.ScopeRoom, nil
	}
	return "", fmt.Errorf("give one of --machine, --room and --session; --session with --room finds the session in that room")
}

func describeSubscription(info contract.SubscriptionInfo) string {
	sub := info.Subscription
	when := "every " + string(sub.Every)
	if sub.Every == "" {
		names := make([]string, 0, len(sub.On))
		for _, kind := range sub.On {
			names = append(names, string(kind))
		}
		when = "on " + strings.Join(names, ",")
		for _, clause := range sub.Where {
			when += " where " + clause
		}
	}
	action := "wake"
	if sub.Run != nil {
		action = "run " + sub.Run.Image + ": " + strings.Join(sub.Run.Command, " ")
		if sub.Run.Host != "" {
			action = "run " + sub.Run.Image + " on " + string(sub.Run.Host) + ": " + strings.Join(sub.Run.Command, " ")
		}
	} else if sub.Wake != "" {
		action = "wake " + string(sub.Wake)
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

// printEventTypes lists what --on accepts, each with what it is.
func printEventTypes(out io.Writer) error {
	for _, kind := range contract.EventTypes() {
		if !room.Deliverable(kind) {
			continue
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\n", kind, contract.EventDoc(kind)); err != nil {
			return err
		}
	}
	return nil
}

func subscriptionCommand(get func() *socketClient) *cobra.Command {
	root := &cobra.Command{Use: "subscription", Short: "Typed subscriptions: wake a session or run a small box on events or on a schedule"}
	var machine bool
	var room, session, image, size, host, wake, every string
	var on, where, files []string
	add := &cobra.Command{Use: "add (--machine | --room ROOM | [--room ROOM] --session SESSION) (--on TYPE [--where FILTER] | --every DURATION) (--wake SESSION | [--image NAME] [--size SIZE] [--host HOST] [--file BOXPATH=LOCALPATH] -- COMMAND [ARG...])", Short: "Add a subscription",
		Long: `Add a subscription: an event or a schedule, any filters, and one action, which wakes a session or runs a small box.

--every takes a duration such as 1h, 15m or 90s, of at least 1m. --session and --wake take a session's name in --room, or its
ses_… ID.

--where keeps only the github.* and git.* events it matches, and the engine checks it before anything starts. Each --where is one of
key=value, key!=value or text~value, and all of them must hold. Keys are repo, author, actor, label, kind and number. kind is a change kind of github.pr.changed, such as pushed (a tracked PR's head moved, told to subscribers and not to its owner), ci_failed or merged. text~ is a plain
substring of a comment or a body. A field the event does not have matches neither = nor !=.

A Run fired by an event gets that event as JSON on stdin, with DYNA_EVENT set to its type, one Run per event. A scheduled Run gets
empty stdin and no DYNA_EVENT. Any text with "outside": true beside it came from outside and is untrusted.

dyna subscription events lists the event types.`,
		Example: `  dyna subscription add --room ROOM --session upstream-sync --every 1h --wake upstream-sync
  dyna subscription add --room ROOM --on github.pr.opened --where author=levzzz5154 --wake SESSION
  dyna subscription add --room ROOM --on github.pr.changed --where kind=pushed --where repo=horizon-rust/horizon --wake SESSION
  dyna subscription add --room ROOM --on github.mention --where actor!=arismoko --wake SESSION
  dyna subscription add --room ROOM --on github.comment --where 'text~#4953' --where repo=horizon-rust/horizon --wake SESSION
  dyna subscription add --room ROOM --on git.main_updated --where repo=horizon-rust/horizon --image horizon --file upstream-watch.sh=upstream-watch.sh -- bash upstream-watch.sh`,
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := subscriptionScope(machine, room, session)
			if err != nil {
				return err
			}
			req := contract.SubscriptionAddRequest{Scope: scope, Room: contract.RoomID(room), Session: session, Where: where, Every: contract.Duration(every), Wake: wake}
			for _, kind := range on {
				req.On = append(req.On, contract.EventType(kind))
			}
			if len(args) > 0 {
				run := contract.RunSpec{Command: args, Image: image, Size: contract.BoxSizeName(size), Host: contract.HostName(host)}
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
	add.Flags().StringVar(&room, "room", "", "Room scope, or with --session the session's room")
	add.Flags().StringVar(&session, "session", "", "Session scope: a session name in --room, or a ses_… ID")
	add.Flags().StringArrayVar(&on, "on", nil, "Event type (repeatable); dyna subscription events lists them")
	add.Flags().StringArrayVar(&where, "where", nil, "Filter as key=value, key!=value or text~value (repeatable; all must hold)")
	add.Flags().StringVar(&every, "every", "", "Schedule as a duration such as 1h, 15m or 90s, of at least 1m")
	add.Flags().StringVar(&wake, "wake", "", "Session to wake: a name in --room, or a ses_… ID")
	add.Flags().StringVar(&image, "image", "base", "Run image: base, or an image folder of the room's default repository")
	add.Flags().StringVar(&size, "size", string(contract.BoxSizeSmall), "Run box size: small or agent")
	add.Flags().StringVar(&host, "host", "", "Run on this host only, for a script that needs file locks; any host by default")
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
	events := &cobra.Command{Use: "events", Short: "List the event types a subscription can name", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return printEventTypes(cmd.OutOrStdout())
	}}
	root.AddCommand(add, list, remove, events)
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
