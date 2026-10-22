package callerid

import (
	"net"
	"time"

	"golang.org/x/sys/unix"

	"dyna-agent/internal/enginesocket"
)

// enginePID is the PID of the engine that listens on the socket, from the
// kernel's peer credentials, or 0 when no engine answers.
func enginePID() int {
	path, err := enginesocket.Path()
	if err != nil {
		return 0
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return 0
	}
	defer conn.Close()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	_ = raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
			pid = int(cred.Pid)
		}
	})
	return pid
}
