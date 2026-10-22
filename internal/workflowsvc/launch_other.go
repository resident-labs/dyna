//go:build !linux

package workflowsvc

import "os/exec"

func foregroundParent(*exec.Cmd) func() { return func() {} }
