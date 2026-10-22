package processguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// SO_PEERPIDFD is Linux 6.5's socket peer handle, not yet named by x/sys.
const socketPeerPIDFD = 77

func socketPeer(conn net.Conn) (*Peer, error) {
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, errors.New("private process peer is not a Unix connection")
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	peer := &Peer{fd: -1}
	var credentialError error
	if err := raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credentialError = err
			return
		}
		peer.pid = int(credentials.Pid)
		if pinned, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, socketPeerPIDFD); err == nil {
			peer.fd = pinned
		}
	}); err != nil {
		credentialError = errors.Join(credentialError, err)
	}
	if credentialError != nil {
		peer.Close()
		return nil, fmt.Errorf("read private socket peer: %w", credentialError)
	}
	if peer.pid <= 0 {
		peer.Close()
		return nil, errors.New("private socket has no visible peer PID")
	}
	peer.birth, err = processBirth(peer.pid)
	if err != nil {
		peer.Close()
		return nil, err
	}
	if peer.Pinned() && !peer.Alive() {
		peer.Close()
		return nil, errors.New("private socket peer exited during identification")
	}
	return peer, nil
}

func (p *Peer) Alive() bool {
	if !p.Pinned() {
		return false
	}
	// Signal 0 also succeeds for an exited, unreaped process. Poll the same
	// pinned exit event that KillAndWait uses; reaping is not part of liveness.
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n == 0
}

func (p *Peer) Close() error {
	if !p.Pinned() {
		return nil
	}
	err := unix.Close(p.fd)
	p.fd = -1
	return err
}

func (p *Peer) killAndWait(ctx context.Context) error {
	if p.pid == os.Getpid() {
		return errors.New("refuse to retire this process as its own socket peer")
	}
	if err := unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	cancelFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return err
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_, _ = unix.Write(cancelFD, []byte{1, 0, 0, 0, 0, 0, 0, 0})
		close(cancelled)
	})
	defer func() {
		if !stop() {
			<-cancelled
		}
		unix.Close(cancelFD)
	}()
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}, {Fd: int32(cancelFD), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			return nil
		}
		if fds[1].Revents != 0 {
			return ctx.Err()
		}
		return errors.New("private peer exit could not be proved")
	}
}
