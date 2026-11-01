// Package egress is the only way out of a CI runner's empty network namespace.
// It tunnels HTTPS to public addresses and refuses everything a job must not
// reach: loopback, private and link-local ranges, and tailnet addresses.
package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// notPublic lists ranges Go's address predicates treat as global unicast.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // Tailscale and other carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can lead back to a private IPv4 address.
	netip.MustParsePrefix("2002::/16"),    // So can 6to4.
}

// Public reports whether a job may reach addr.
func Public(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, prefix := range notPublic {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var errRefused = errors.New("address is not public")

// checkAddress runs after name resolution and before connecting, so a public
// name that resolves to a private address is refused too.
func checkAddress(_, address string, _ syscall.RawConn) error {
	parsed, err := netip.ParseAddrPort(address)
	if err != nil || !Public(parsed.Addr()) {
		return errRefused
	}
	return nil
}

// Dial connects to a CONNECT target.
type Dial func(context.Context, string, string) (net.Conn, error)

// PublicDial refuses every non-public address.
func PublicDial() Dial {
	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: checkAddress}
	return dialer.DialContext
}

// Serve answers HTTP CONNECT on listener until ctx ends.
func Serve(ctx context.Context, listener net.Listener, dial Dial) error {
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handle(ctx, conn, dial)
	}
}

func handle(ctx context.Context, conn net.Conn, dial Dial) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	_, port, err := net.SplitHostPort(request.Host)
	if err != nil || request.Method != http.MethodConnect || port != "443" {
		reply(conn, http.StatusForbidden)
		return
	}
	upstream, err := dial(ctx, "tcp", request.Host)
	if err != nil {
		if errors.Is(err, errRefused) {
			reply(conn, http.StatusForbidden)
		} else {
			reply(conn, http.StatusBadGateway)
		}
		return
	}
	defer upstream.Close()
	_ = conn.SetDeadline(time.Time{})
	reply(conn, http.StatusOK)
	done := make(chan struct{})
	go func() {
		// A job's long poll and a large download stay open as long as they need.
		_, _ = io.Copy(upstream, reader)
		if half, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(conn, upstream)
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	}
	<-done
}

func reply(conn net.Conn, status int) {
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n\r\n", status, http.StatusText(status))
}
