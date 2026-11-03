package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box/computer"
	"github.com/spf13/cobra"
)

// computerCommand is the box's dyna computer: the agent's computer actions, which main gives their help from the tool
// registry, and the start and stream the engine runs for the owner's view.
func computerCommand(socket string) *cobra.Command {
	root := &cobra.Command{Use: "computer"}
	agent := computer.NewAgent(socket)
	var req computer.Request
	var x, y, holdMS, stepMS, ms int
	// do runs action with the request the flags and build made, and prints its text; a screenshot goes to file.
	do := func(action computer.Action, build func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			if build != nil {
				if err := build(cmd, args); err != nil {
					return err
				}
			}
			flags := cmd.Flags()
			if flags.Changed("x") {
				req.X = &x
			}
			if flags.Changed("y") {
				req.Y = &y
			}
			if flags.Changed("hold-ms") {
				req.HoldMS = &holdMS
			}
			if flags.Changed("step-ms") {
				req.StepMS = &stepMS
			}
			limit := 30 * time.Second
			if action == computer.Launch {
				limit += time.Duration(req.TimeoutMS) * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), limit)
			defer cancel()
			result, err := computer.Do(ctx, agent, action, req)
			if err != nil {
				return err
			}
			if result.PNG != nil {
				if err := os.WriteFile(args[0], result.PNG, 0644); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Text)
			return err
		}
	}
	command := func(use string, args cobra.PositionalArgs, action computer.Action, build func(*cobra.Command, []string) error) *cobra.Command {
		c := &cobra.Command{Use: use, Args: args, RunE: do(action, build)}
		if action != computer.Text && action != computer.Wait && action != computer.Screenshot {
			c.Flags().StringVar(&req.Status, "status", "", "A few words on what this is for, shown to the owner watching")
		}
		return c
	}
	point := func(c *cobra.Command) {
		c.Flags().IntVar(&x, "x", 0, "Desktop x")
		c.Flags().IntVar(&y, "y", 0, "Desktop y")
	}
	held := func(c *cobra.Command) {
		c.Flags().StringSliceVar(&req.Keys, "keys", nil, "Keys held down meanwhile, such as SHIFT")
	}
	coordinates := func(arg string) (computer.Point, error) {
		a, b, ok := strings.Cut(arg, ",")
		px, e1 := strconv.Atoi(a)
		py, e2 := strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil {
			return computer.Point{}, fmt.Errorf("a point is X,Y: %q", arg)
		}
		return computer.Point{X: px, Y: py}, nil
	}

	start := &cobra.Command{Use: "start", Short: "Start the desktop and print the DevTools URL", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		started, err := agent.Start(ctx)
		if err != nil {
			return err
		}
		if started.Exit != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), started.Exit)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), started.URL)
		return err
	}}
	open := command("open URL", cobra.ExactArgs(1), computer.Open, func(_ *cobra.Command, args []string) error { req.URL = args[0]; return nil })
	click := command("click [CSS]", cobra.MaximumNArgs(1), computer.Click, func(_ *cobra.Command, args []string) error {
		if len(args) == 1 {
			req.Selector = args[0]
		}
		return nil
	})
	point(click)
	held(click)
	click.Flags().StringVar(&req.Button, "button", "", "left, middle or right")
	click.Flags().BoolVar(&req.Double, "double", false, "A double click")
	move := command("move X Y", cobra.ExactArgs(2), computer.Move, func(cmd *cobra.Command, args []string) error {
		at, err := coordinates(args[0] + "," + args[1])
		x, y = at.X, at.Y
		_ = cmd.Flags().Set("x", args[0])
		_ = cmd.Flags().Set("y", args[1])
		return err
	})
	point(move)
	drag := command("drag X,Y X,Y...", cobra.RangeArgs(2, 200), computer.Drag, func(_ *cobra.Command, args []string) error {
		for _, arg := range args {
			at, err := coordinates(arg)
			if err != nil {
				return err
			}
			req.Path = append(req.Path, at)
		}
		return nil
	})
	drag.Flags().IntVar(&stepMS, "step-ms", 0, "The pause after each point")
	held(drag)
	scroll := command("scroll", cobra.NoArgs, computer.Scroll, nil)
	point(scroll)
	held(scroll)
	scroll.Flags().IntVar(&req.ScrollX, "scroll-x", 0, "Wheel notches right, or left when negative")
	scroll.Flags().IntVar(&req.ScrollY, "scroll-y", 0, "Wheel notches down, or up when negative")
	key := command("key KEY...", cobra.MinimumNArgs(1), computer.Key, func(_ *cobra.Command, args []string) error { req.Keys = args; return nil })
	key.Flags().IntVar(&holdMS, "hold-ms", 0, "How long the keys stay down")
	typ := command("type [CSS] TEXT", cobra.RangeArgs(1, 2), computer.Type, func(_ *cobra.Command, args []string) error {
		req.Text = args[len(args)-1]
		if len(args) == 2 {
			req.Selector = args[0]
		}
		return nil
	})
	text := command("text [CSS]", cobra.MaximumNArgs(1), computer.Text, func(_ *cobra.Command, args []string) error {
		if len(args) == 1 {
			req.Selector = args[0]
		}
		return nil
	})
	wait := command("wait [MS]", cobra.MaximumNArgs(1), computer.Wait, func(_ *cobra.Command, args []string) error {
		if len(args) == 1 {
			n, err := strconv.Atoi(args[0])
			ms = n
			req.MS = &ms
			return err
		}
		return nil
	})
	shot := command("screenshot FILE", cobra.ExactArgs(1), computer.Screenshot, nil)
	point(shot)
	shot.Flags().IntVar(&req.Width, "width", 0, "A region's width")
	shot.Flags().IntVar(&req.Height, "height", 0, "A region's height")
	shot.Flags().BoolVar(&req.Page, "page", false, "Chromium's whole page instead of the desktop")
	launch := command("launch COMMAND", cobra.ExactArgs(1), computer.Launch, func(_ *cobra.Command, args []string) error { req.Command = args[0]; return nil })
	launch.Flags().IntVar(&req.TimeoutMS, "timeout-ms", 0, "How long to wait for the new window")
	stop := command("stop", cobra.NoArgs, computer.Stop, nil)
	// The engine runs this through podman exec for the owner's view; it does not start the desktop.
	stream := &cobra.Command{Use: "stream", Short: "Carry the desktop's VNC stream over stdin and stdout", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return computer.Stream(cmd.Context(), computer.VNCSocket, cmd.InOrStdin(), cmd.OutOrStdout())
	}}
	root.AddCommand(start, open, click, move, drag, scroll, key, typ, text, wait, shot, launch, stop, stream)
	return root
}
