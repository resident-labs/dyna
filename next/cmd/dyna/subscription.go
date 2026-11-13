package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/workflowmeta"
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
	} else if sub.Workflow != nil {
		meta, _ := workflowmeta.Parse(sub.Workflow.Script)
		action = "workflow " + meta.Name
		if sub.Key != "" {
			action += " per " + sub.Key
		}
	} else if sub.Wake != "" {
		action = "wake " + string(sub.Wake)
	}
	state := []string{}
	if sub.Workflow != nil {
		state = append(state, fmt.Sprintf("%d waiting", info.Waiting))
	}
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
	var machine, latest, includeOwn bool
	var workflow, workflowArgs, key, parent string
	var room, session, image, size, host, wake, every string
	var on, where, files []string
	add := &cobra.Command{Use: "add (--machine | --room ROOM | [--room ROOM] --session SESSION) (--on TYPE [--where FILTER] | --every DURATION) (--wake SESSION | [--image NAME] [--size SIZE] [--host HOST] [--file BOXPATH=LOCALPATH] -- COMMAND [ARG...])", Short: "Add a subscription",
		Long: `List event types with dyna subscription events. Add a subscription: an event or a schedule, any filters, and one action, which wakes a session or runs a small box.

--every takes a duration such as 1h, 15m or 90s, of at least 1m. --session and --wake take a session's name in --room, or its
ses_… ID.

--where keeps only the github.* and git.* events it matches, and the engine checks it before anything starts. Each --where is one of
key=value, key!=value or text~value, and all of them must hold. Keys are repo, author, actor, label, kind and number. kind is a change kind of github.pr.changed, such as pushed, ci_failed or merged; kind=pushed,opened matches either. github.pr.changed covers tracked PRs and every open PR with the repository room setting prEvents on (owner CLI on the engine host: dyna room repo add ROOM REPO --pr-events on). pushed carries push.before and push.after. text~ is a plain
substring of a comment or a body. A field the event does not have matches neither = nor !=.

A Run fired by an event gets that event as JSON on stdin, with DYNA_EVENT set to its type, one Run per event. A scheduled Run gets
empty stdin and no DYNA_EVENT. Any text with "outside": true beside it came from outside the room, and reaches an agent inside <outside source="…" author="…"> and </outside>.

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
			req := contract.SubscriptionAddRequest{Scope: scope, Room: contract.RoomID(room), Session: session, Where: where, Every: contract.Duration(every), Wake: wake, Key: key, Latest: latest, Parent: contract.SessionID(parent), IncludeOwn: includeOwn}
			if workflow == "" && workflowArgs != "" {
				return fmt.Errorf("--args needs --workflow")
			}

			if workflow != "" {
				script, err := os.ReadFile(workflow)
				if err != nil {
					return err
				}
				spec := &contract.WorkflowSubscription{Script: string(script)}
				if workflowArgs != "" {
					if err := json.Unmarshal([]byte(workflowArgs), &spec.Args); err != nil {
						return err
					}
				}
				req.Workflow = spec
			}
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
	add.Flags().StringVar(&workflow, "workflow", "", "Workflow script path in the repository or shared folder")
	add.Flags().StringVar(&workflowArgs, "args", "", "Workflow args as a JSON object")
	add.Flags().StringVar(&key, "key", "", "Serial key: literal, or template with {repo} and {number}")
	add.Flags().StringVar(&parent, "parent", "", "Workflow parent session; root by default")
	add.Flags().BoolVar(&latest, "latest", false, "Keep only the newest waiting event for a key; the running run is never replaced")
	add.Flags().BoolVar(&includeOwn, "include-own", false, "Allow comments, reviews and metadata by the GitHub account Dyna acts as in this room")
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
	var fireKey, event string
	fire := &cobra.Command{Use: "fire ID", Short: "Queue an event workflow with real state (root or owner)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.SubscriptionFireRequest{ID: contract.SubscriptionID(args[0])}
		if cmd.Flags().Changed("key") {
			req.Key = &fireKey
		}
		if cmd.Flags().Changed("event") {
			if !json.Valid([]byte(event)) {
				return fmt.Errorf("--event must be JSON")
			}
			req.Event = json.RawMessage(event)
		}
		_, err := call[contract.SubscriptionFireRequest, contract.SubscriptionFireResponse](cmd.Context(), get(), contract.MethodSubscriptionFire, req)
		return err
	}}
	fire.Flags().StringVar(&fireKey, "key", "", "Override the resolved serial key")
	fire.Flags().StringVar(&event, "event", "", "Full event envelope as JSON; omitted sends hook.due")
	var updateWorkflow, updateArgs, updateKey, updateParent string
	var updateWhere []string
	var updateLatest bool
	update := &cobra.Command{Use: "update ID", Short: "Change a workflow without losing state or queued events", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.SubscriptionUpdateRequest{ID: contract.SubscriptionID(args[0])}
		if cmd.Flags().Changed("args") && !cmd.Flags().Changed("workflow") {
			return fmt.Errorf("--args needs --workflow")
		}
		if cmd.Flags().Changed("workflow") {
			script, err := os.ReadFile(updateWorkflow)
			if err != nil {
				return err
			}
			req.Workflow = &contract.WorkflowSubscription{Script: string(script)}
			if cmd.Flags().Changed("args") {
				if err := json.Unmarshal([]byte(updateArgs), &req.Workflow.Args); err != nil {
					return err
				}
			}
		}
		if cmd.Flags().Changed("where") {
			if len(updateWhere) == 1 && updateWhere[0] == "" {
				updateWhere = []string{}
			}
			req.Where = &updateWhere
		}
		if cmd.Flags().Changed("key") {
			req.Key = &updateKey
		}
		if cmd.Flags().Changed("latest") {
			req.Latest = &updateLatest
		}
		if cmd.Flags().Changed("parent") {
			parent := contract.SessionID(updateParent)
			req.Parent = &parent
		}
		_, err := call[contract.SubscriptionUpdateRequest, contract.SubscriptionUpdateResponse](cmd.Context(), get(), contract.MethodSubscriptionUpdate, req)
		return err
	}}
	update.Flags().StringVar(&updateWorkflow, "workflow", "", "Replacement script file")
	update.Flags().StringVar(&updateArgs, "args", "", "Replacement workflow args as a JSON object")
	update.Flags().StringArrayVar(&updateWhere, "where", nil, "Replacement filters (repeatable); an empty value clears them")
	update.Flags().StringVar(&updateKey, "key", "", "Replacement serial key")
	update.Flags().BoolVar(&updateLatest, "latest", false, "Keep only the newest waiting event per key")
	update.Flags().StringVar(&updateParent, "parent", "", "Parent session; empty means root")
	var stateKey, value string
	state := &cobra.Command{Use: "state ID", Short: "List or read state; --value null resets a key", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		req := contract.SubscriptionStateRequest{ID: contract.SubscriptionID(args[0])}
		if cmd.Flags().Changed("key") {
			req.Key = &stateKey
		}
		if cmd.Flags().Changed("value") {
			if req.Key == nil {
				return fmt.Errorf("--value needs --key")
			}
			if !json.Valid([]byte(value)) {
				return fmt.Errorf("--value must be JSON")
			}
			req.Value = json.RawMessage(value)
		}
		return printCall[contract.SubscriptionStateResponse](cmd, get(), contract.MethodSubscriptionState, req)
	}}
	state.Flags().StringVar(&stateKey, "key", "", "Read this key; omitted lists all keys")
	state.Flags().StringVar(&value, "value", "", "Write JSON to the key; null resets it")
	root.AddCommand(add, list, remove, events, fire, update, state)

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
