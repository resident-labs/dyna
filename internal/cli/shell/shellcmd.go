// Package shell provides `dyna shell`: dyna-managed background commands.
package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/shelldisplay"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/subagent"
)

// shellStopView is what `dyna shell stop` prints. Exit is null until the shell
// has ended, as it was when this reply was a map.
type shellStopView struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Exit   *shells.Exit `json:"exit"`
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell",
		Short: "Inspect and stop shell jobs",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
		Long: "Agents start shell jobs with the shell tool of Dyna's MCP server.\n" +
			"Use list, tail and stop to manage them.",
	}
	cmd.AddCommand(listCmd(), tailCmd(), stopCmd(), runCmd(), spawnCmd())
	return cmd
}

func printJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func listCmd() *cobra.Command {
	var all, asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List running shells (--all includes finished ones)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st := shells.DefaultStore()
			if err := st.Sweep(); err != nil {
				return err
			}
			list, err := st.List(all)
			if err != nil {
				return err
			}
			if asJSON {
				if list == nil {
					list = []shells.Info{}
				}
				return printJSON(c.OutOrStdout(), list)
			}
			for _, in := range list {
				code := ""
				if in.Exit != nil {
					code = fmt.Sprintf(" exit %d", in.Exit.Code)
				}
				fmt.Fprintf(c.OutOrStdout(), "%s  %-10s %-8s%s  %v\n", in.ID, in.Name, in.Status, code, in.Cmd)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "include finished shells")
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")
	return c
}

func tailCmd() *cobra.Command {
	var n int
	var follow bool
	c := &cobra.Command{
		Use:   "tail <id|name>",
		Short: "Print the last lines of a shell's output",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st := shells.DefaultStore()
			in, err := st.Resolve(args[0])
			if err != nil {
				return err
			}
			text, err := st.Tail(in.ID, n)
			if err != nil {
				return err
			}
			if text != "" {
				fmt.Fprintln(c.OutOrStdout(), text)
			}
			if !follow {
				return nil
			}
			path, _ := st.OutputPath(in.ID)
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			off, _ := f.Seek(0, io.SeekEnd)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			buf := make([]byte, 64*1024)
			for {
				nr, _ := f.ReadAt(buf, off)
				if nr > 0 {
					c.OutOrStdout().Write(buf[:nr]) //nolint:errcheck
					off += int64(nr)
					continue
				}
				if cur, _ := st.Info(in.ID); cur.Status == "exited" || cur.Status == "lost" {
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(200 * time.Millisecond):
				}
			}
		},
	}
	c.Flags().IntVarP(&n, "lines", "n", 40, "number of lines")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output until the shell exits")
	return c
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <id|name>",
		Short: "Stop a shell (SIGTERM to its process group, SIGKILL after 5 s)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st := shells.DefaultStore()
			in, err := st.Resolve(args[0])
			if err != nil {
				return err
			}
			_, stamped := mode.CurrentSession()
			if stamped {
				if in.Owner == "" {
					return fmt.Errorf("shell %s is owned by the room owner", in.ID)
				}
				if _, err := subagent.NewManager().Target(in.Owner); err != nil {
					return fmt.Errorf("shell %s is owned by %s: %w", in.ID, in.Owner, err)
				}
			}
			by := "owner"
			if stamped {
				by = "agent"
			}
			in, err = st.Stop(in.ID, by)
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), shellStopView{ID: in.ID, Status: in.Status, Exit: in.Exit})
		},
	}
}

func spawnCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_spawn <id>",
		Short:  "internal: start one shell supervisor and exit",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			return shells.StartSupervisor(exe, args[0])
		},
	}
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "_run <id>",
		Short:  "internal: supervise one shell",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			st := shells.DefaultStore()
			// A shell must not reach the owner display or start a computer on
			// its own; the policy needs the computer package, which imports
			// shells, so the shell CLI applies it here.
			st.CommandEnv = shelldisplay.CommandEnv
			if err := st.Supervise(ctx, args[0]); err != nil {
				return err
			}
			return notifyExit(st, args[0])
		},
	}
}

// notifyExit tells the owner how a job ended, unless the owner already saw it.
func notifyExit(st *shells.Store, id string) error {
	unseen, err := st.ClaimNotice(id)
	if err != nil || !unseen {
		return err
	}
	info, err := st.Info(id)
	if err != nil {
		return err
	}
	exit := notify.ShellExit{ID: info.ID, RoomID: info.RoomID, Description: info.Name, Code: -1}
	if len(info.Cmd) > 0 {
		exit.Command = filepath.Base(info.Cmd[0])
	}
	if info.Exit != nil {
		exit.Code = info.Exit.Code
		exit.Duration = info.Exit.Ended.Sub(info.Started)
	}
	if runstore.IsSessionDirID(info.Owner) {
		exit.Parent = info.Owner
	}
	if exit.Tail, err = st.Tail(id, 20); err != nil {
		return err
	}
	if len(exit.Tail) > 2048 {
		exit.Tail = strings.ToValidUTF8(exit.Tail[len(exit.Tail)-2048:], "")
	}
	_, err = notify.Default().CompleteShell(context.Background(), exit)
	return err
}
