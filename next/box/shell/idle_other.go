//go:build !linux

package shell

import "os/exec"

// StartIdle starts cmd; off Linux there is no idle policy.
func StartIdle(cmd *exec.Cmd) error { return cmd.Start() }
