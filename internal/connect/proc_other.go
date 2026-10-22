//go:build !linux

package connect

import "os/exec"

// dieWithParent has no portable equivalent outside Linux; serve still
// stops cloudflared on a clean shutdown through the context.
func dieWithParent(cmd *exec.Cmd) {}
