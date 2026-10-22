package callerid

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// soPeerPIDFD is SO_PEERPIDFD (Linux 6.5), which x/sys does not name yet.
// 77 is the asm-generic value, the same on amd64 and arm64; check it on any
// other architecture.
const soPeerPIDFD = 77

// ResolveConn identifies the process at the other end of a Unix socket
// connection, right after accept. The kernel gives its PID, and on Linux 6.5
// and later a pidfd as well: a caller whose process ended while /proc was
// read is refused, because its PID may then name another process.
func (r Resolver) ResolveConn(conn net.Conn) Identity {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return refused("the caller is not on the engine's Unix socket")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return refused("cannot read the caller's socket: %v", err)
	}
	pid, pidfd := 0, -1
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credErr = err
			return
		}
		pid = int(cred.Pid)
		if p, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, soPeerPIDFD); err == nil {
			pidfd = p
		}
	}); err != nil {
		credErr = errors.Join(credErr, err)
	}
	if credErr != nil || pid <= 0 {
		return refused("cannot read the caller's process from its socket: %v", credErr)
	}
	if pidfd >= 0 {
		defer unix.Close(pidfd)
	}
	id := r.Resolve(pid)
	if pidfd >= 0 && unix.PidfdSendSignal(pidfd, 0, nil, 0) != nil {
		return refused("the caller exited while the engine identified it, so its PID may name another process")
	}
	return id
}
