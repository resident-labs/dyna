//go:build !linux

package shell

import "os/exec"

// StartLowPriority starts cmd; off Linux it sets no priority.
func StartLowPriority(cmd *exec.Cmd) error { return cmd.Start() }
