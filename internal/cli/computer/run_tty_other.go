//go:build !linux

package computer

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

func foregroundSignals() []os.Signal                              { return []os.Signal{os.Interrupt} }
func configureForeground(_ *exec.Cmd, _ bool, _ io.Reader) func() { return func() {} }

func foregroundExitCode(err *exec.ExitError) int { return err.ExitCode() }
func runTTY(_ *cobra.Command, _ *exec.Cmd) error { return fmt.Errorf("computer TTY requires Linux") }
