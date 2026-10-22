//go:build linux

package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func init() {
	waitE2ERunner = func(ctx context.Context, pid int, identity string) error {
		fd, err := unix.PidfdOpen(pid, 0)
		if err == unix.ESRCH {
			return nil
		}
		if err != nil {
			return err
		}
		defer unix.Close(fd)
		// Pin the PID before checking its identity. Never wait for a reused PID.
		cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if os.IsNotExist(err) || (err == nil && !strings.Contains(strings.ReplaceAll(string(cmdline), "\x00", " "), identity)) {
			return nil
		}
		if err != nil {
			return err
		}
		deadline, _ := ctx.Deadline()
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, max(1, int(time.Until(deadline).Milliseconds())))
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return err
			}
			if n > 0 {
				return nil // Exit is observable even while the runner is a zombie.
			}
		}
	}
}
