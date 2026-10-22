package computer

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/cli/exitcode"
	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

func newRunCommand() *cobra.Command {
	var intent, cwd string
	var tty bool
	c := &cobra.Command{Use: "run [flags] -- <command> [args...]", Short: "Run a foreground command in your computer environment", Long: "Run a foreground command with streamed output and its exit status. Use --tty for interactive terminal programs. GUI apps that should remain open use computer open.", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		status, err := computers.RequiredStatus(intent)
		if err != nil {
			return err
		}
		if cwd != "" {
			info, err := os.Stat(cwd)
			if err != nil {
				return fmt.Errorf("cwd: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("cwd is not a directory: %s", cwd)
			}
		}
		ctx, stop := signal.NotifyContext(c.Context(), foregroundSignals()...)
		defer stop()
		id, room, err := resolveActor()
		if err != nil {
			return err
		}
		if _, err := computers.Ensure(ctx, id, room); err != nil {
			return err
		}
		var meta computers.Meta
		if err := computers.NewClient(id).Call(ctx, computers.Request{Op: "run.prepare", Status: status}, &meta); err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		restore := configureForeground(cmd, tty, c.InOrStdin())
		cmd.Dir = cwd
		env, err := computers.Environment(meta, cmd.Environ())
		if err != nil {
			return err
		}
		cmd.Env = append(env, "DYNA_COMPUTER_PROCESS="+id)
		cmd.WaitDelay = time.Second
		childenv.Clean(cmd)
		if tty {
			err = runTTY(c, cmd)
		} else {
			cmd.Stdin, cmd.Stdout, cmd.Stderr = c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr()
			err = cmd.Run()
			restore()
		}
		var failed *exec.ExitError
		if errors.As(err, &failed) {
			return exitcode.New(foregroundExitCode(failed), failed.Error())
		}
		return err
	}}
	c.Flags().SetInterspersed(false)
	c.Flags().StringVar(&intent, "status", "", "required: short intent (maximum 80 characters)")
	c.Flags().StringVar(&cwd, "cwd", "", "working directory (default: current)")
	c.Flags().BoolVarP(&tty, "tty", "t", false, "allocate a terminal; stdout and stderr share its output")
	return c
}
