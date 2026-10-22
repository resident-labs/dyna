//go:build linux

package computers

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Relay forwards each recvmsg payload with its SCM_RIGHTS descriptors. The
// receiver owns new descriptor copies; this process closes its copies even
// when sendmsg fails. A short send is completed before the next recvmsg.
func Relay(a, b *net.UnixConn) error {
	var wg sync.WaitGroup
	var first error
	var mu sync.Mutex
	copyOne := func(dst, src *net.UnixConn) {
		defer wg.Done()
		err := relayOne(dst, src)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
			mu.Lock()
			if first == nil {
				first = err
			}
			mu.Unlock()
		}
		a.Close()
		b.Close()
	}
	wg.Add(2)
	go copyOne(a, b)
	go copyOne(b, a)
	wg.Wait()
	return first
}
func relayOne(dst, src *net.UnixConn) error {
	buf := make([]byte, 64*1024)
	oob := make([]byte, unix.CmsgSpace(253*4))
	for {
		n, oobn, flags, _, err := src.ReadMsgUnix(buf, oob)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.EOF
		}
		messages, parseErr := unix.ParseSocketControlMessage(oob[:oobn])
		fds := []int{}
		for _, m := range messages {
			if m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_RIGHTS {
				received, e := unix.ParseUnixRights(&m)
				if e != nil {
					parseErr = e
				}
				for _, fd := range received {
					unix.CloseOnExec(fd)
				}
				fds = append(fds, received...)
			}
		}
		if flags&unix.MSG_CTRUNC != 0 {
			parseErr = errors.New("relay control message truncated")
		}
		if parseErr != nil {
			for _, fd := range fds {
				unix.Close(fd)
			}
			return parseErr
		}
		control := []byte(nil)
		if len(fds) > 0 {
			control = unix.UnixRights(fds...)
		}
		sent, _, err := dst.WriteMsgUnix(buf[:n], control, nil)
		for _, fd := range fds {
			unix.Close(fd)
		}
		if err != nil {
			return err
		}
		for sent < n {
			m, e := dst.Write(buf[sent:n])
			sent += m
			if e != nil {
				return e
			}
		}
	}
}

// unixListener disables Go's automatic unlink so cleanup is explicit.
func unixListener(path string) (*net.UnixListener, error) {
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err == nil {
		l.SetUnlinkOnClose(false)
		if len(path) > 0 && path[0] != '@' {
			err = os.Chmod(path, 0600)
			if err != nil {
				l.Close()
			}
		}
	}
	return l, err
}
