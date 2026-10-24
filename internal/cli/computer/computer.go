// Package computer provides the agent's computer CLI and shared daemon entry.
package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/computers"
	"dyna-agent/internal/devicesvc"
	"dyna-agent/internal/mode"
	roompkg "dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"github.com/spf13/cobra"
)

// ensureComputer registers the acting computer, creating it on first use.
func ensureComputer(c *cobra.Command) (computers.Meta, error) {
	id, roomID, err := resolveActor()
	if err != nil {
		return computers.Meta{}, err
	}
	return computers.Ensure(c.Context(), id, roomID)
}

// postAttachment posts text with one file to room mentions, using the dyna post command.
func postAttachment(c *cobra.Command, mentions []string, text, path string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append([]string{"post"}, mentions...)
	args = append(args, text, "--attach", path)
	cmd := exec.CommandContext(c.Context(), exe, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = c.ErrOrStderr()
	cmd.Stderr = c.ErrOrStderr()
	return childenv.Clean(cmd).Run()
}

func printJSON(c *cobra.Command, v any) error { return json.NewEncoder(c.OutOrStdout()).Encode(v) }
func NewDaemonCommand() *cobra.Command {
	root := &cobra.Command{Use: "computers", Hidden: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return computers.RunDaemon(ctx, computers.SupervisorOptions{})
	}}
	root.AddCommand(&cobra.Command{Use: "chromium SESSION -- ARGS", Hidden: true, DisableFlagParsing: true, Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		rest := args[1:]
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		return computers.LaunchChromium(c.Context(), args[0], rest)
	}})
	return root
}
func NewCommand() *cobra.Command {
	root := &cobra.Command{Use: "computer", Short: "Inspect and control your Linux computer", Long: "A stamped Dyna session uses its own computer. With no Dyna stamp, root (the room owner) uses the room's root computer. env, shot and act create the computer on first use."}
	// resolve names the acting computer and its room (subagent.Actor).
	resolve := resolveActor
	ensure := ensureComputer
	root.AddCommand(newControlCommand(ensure))
	status := &cobra.Command{Use: "status", Short: "Show computer state without creating a desktop", Long: "Show computer state without creating a desktop. The doctor field explains an unavailable desktop, graphics, or stream; it is omitted when all are running.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		id, roomID, err := resolve()
		if err != nil {
			return err
		}
		v, err := computers.GetStatus(c.Context(), id)
		if err != nil {
			return err
		}
		var streamErr error
		if v.State == computers.StateRunning && v.Renderer != "" && !v.Stream.Running {
			streamErr = computers.NewClient(id).CheckStream(c.Context())
		}
		out := struct {
			computers.Status
			Doctor string `json:"doctor,omitempty"`
			Viewer string `json:"viewer,omitempty"`
		}{Status: v, Doctor: statusDoctor(v, streamErr)}
		// A running computer gets a link a person can open to watch or take control.
		if v.State == computers.StateRunning {
			if origin, err := devicesvc.New(subagent.DefaultStore().Root).AppOrigin(); err == nil {
				out.Viewer = fmt.Sprintf("%s/#pop=%s:%s", strings.TrimSuffix(origin, "/"), roomID, id)
			}
		}
		return printJSON(c, out)
	}}
	var room string
	var listAll, listJSON bool
	list := &cobra.Command{Use: "list", Short: "List live computers in a short table", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		filter := room
		if room != "" {
			if record, err := roompkg.DefaultStore().Resolve(room); err == nil {
				filter = record.ID
			}
		}
		v, err := computers.List(c.Context(), filter)
		if err != nil {
			return err
		}
		return printComputerList(c, v, listAll, listJSON)
	}}
	list.Flags().StringVar(&room, "room", "", "filter room")
	list.Flags().BoolVar(&listAll, "all", false, "include computers with no running desktop")
	list.Flags().BoolVar(&listJSON, "json", false, "print full state as JSON")
	var envSize, envIntent string
	env := &cobra.Command{Use: "env", Short: "Print the display environment for your computer", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		size, intent, err := requestedSize(c, envSize, envIntent)
		if err != nil {
			return err
		}
		m, err := ensure(c)
		if err != nil {
			return err
		}
		if err := applySize(c, m.SessionID, size, intent); err != nil {
			return err
		}
		values, err := computers.Environment(m, os.Environ())
		if err != nil {
			return err
		}
		for _, line := range values {
			key, _, _ := strings.Cut(line, "=")
			switch key {
			case "WAYLAND_DISPLAY", "DISPLAY", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "PATH", "DYNA_OWNER_WAYLAND_DISPLAY", "DYNA_OWNER_DISPLAY":
				fmt.Fprintln(c.OutOrStdout(), line)
			}
		}
		return nil
	}}
	env.Flags().StringVar(&envSize, "size", "", "desktop size "+computers.SizeHelp())
	env.Flags().StringVar(&envIntent, "status", "", "required with --size: short intent")
	var stopIntent string
	stop := &cobra.Command{Use: "stop [COMPUTER]", Short: "Stop your computer, or as root or the owner any computer (requires --status)", Example: "dyna computer stop --status \"Finished checking the page\"\ndyna computer stop ses_20261020-082614-2475f7e8 --status \"Idle for hours\"", PreRunE: func(c *cobra.Command, _ []string) error {
		var err error
		stopIntent, err = computers.RequiredStatus(stopIntent)
		return err
	}, Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		id, err := stopTarget(args, resolve)
		if err != nil {
			return err
		}
		var v computers.Stopped
		err = computers.NewClient(id).Call(c.Context(), computers.Request{Op: "stop", Status: stopIntent}, &v)
		if err != nil {
			return err
		}
		return printJSON(c, v)
	}}
	stop.Flags().StringVar(&stopIntent, "status", "", "required: short intent (maximum 80 characters)")
	var post []string
	shot := &cobra.Command{Use: "shot", Short: "Capture your computer and print its screenshot path", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		m, err := ensure(c)
		if err != nil {
			return err
		}
		id := m.SessionID
		v, err := computers.NewClient(id).Shot(c.Context(), true)
		if err != nil {
			return err
		}
		if len(post) > 0 {
			if v.Path == "" {
				return fmt.Errorf("no windows yet")
			}
			if err := postAttachment(c, post, "Computer screenshot", v.Path); err != nil {
				return err
			}
		}
		return printJSON(c, v)
	}}
	shot.Flags().StringArrayVar(&post, "post", nil, "post screenshot to a room mention (repeatable)")
	var timeout time.Duration
	var intent string
	act := &cobra.Command{Use: "act '<json>'", Short: "Run computer actions with a short --status intent", Long: "Input actions require --status: " + strings.Join(computers.InputActionNames(), ", ") + ". Screenshot and wait batches do not take --status.\n\n" + computers.ActionHelp(), Example: `dyna computer act --status "Checking the search field" '[{"type":"click","x":200,"y":120,"button":"left"}]'`, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		var actions []computers.Action
		if err := json.Unmarshal([]byte(args[0]), &actions); err != nil {
			return err
		}
		var err error
		intent, err = computers.ActionStatus(actions, intent)
		if err != nil {
			return err
		}
		m, err := ensure(c)
		if err != nil {
			return err
		}
		client := computers.NewClient(m.SessionID)
		if s, err := client.Status(c.Context()); err == nil && s.Control == computers.ControlHuman {
			fmt.Fprintln(c.ErrOrStderr(), "A human has control. Agent input waits.")
		}
		ctx, cancel := context.WithCancel(c.Context())
		defer cancel()
		v, err := client.Act(ctx, actions, timeout, intent)
		if err != nil {
			return err
		}
		return printJSON(c, v)
	}}
	act.Flags().StringVar(&intent, "status", "", "required for input: short intent (maximum 80 characters)")
	act.Flags().DurationVar(&timeout, "timeout", 120*time.Second, "maximum wait for human control")
	var openIntent, openSize string
	var openTimeout time.Duration
	open := &cobra.Command{Use: "open [flags] -- <command> [args...]", Short: "Open a non-web app and wait for its window", Long: "Launch a command inside your computer and wait for a new window. Output goes to the returned log path. For web pages, use dyna browser open <url>.", Example: `dyna computer open --status "Checking the terminal" -- foot`, Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		var err error
		openIntent, err = computers.RequiredStatus(openIntent)
		if err != nil {
			return err
		}
		if openTimeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		if err := computers.ValidateAppCommand(args); err != nil {
			return err
		}
		size, intent, err := requestedSize(c, openSize, openIntent)
		if err != nil {
			return err
		}
		m, err := ensure(c)
		if err != nil {
			return err
		}
		if err := applySize(c, m.SessionID, size, intent); err != nil {
			return err
		}
		var result computers.Opened
		if err := computers.NewClient(m.SessionID).Call(c.Context(), computers.Request{Op: "open", Command: args, Status: openIntent, TimeoutMS: openTimeout.Milliseconds()}, &result); err != nil {
			return err
		}
		return printJSON(c, result)
	}}
	open.Flags().StringVar(&openSize, "size", "", "desktop size "+computers.SizeHelp())
	open.Flags().SetInterspersed(false)
	open.Flags().StringVar(&openIntent, "status", "", "required: short intent (maximum 80 characters)")
	open.Flags().DurationVar(&openTimeout, "timeout", 15*time.Second, "maximum wait for a new window")
	root.AddCommand(status, list, env, stop, shot, act, open, newRunCommand(), newRecordCommand(ensure))
	return root
}

// stopTarget is the acting computer, or the named one. Only root or the owner
// may stop another session's computer; IDs come from dyna computer list.
func stopTarget(args []string, resolve func() (string, string, error)) (string, error) {
	if len(args) == 0 {
		id, _, err := resolve()
		return id, err
	}
	if !mode.OwnerProcess() {
		return "", errors.New("only root or the owner can stop another session's computer; run dyna computer stop with no ID to stop your own")
	}
	return args[0], computers.ValidateID(args[0])
}
