// Package egress is the only way out of a CI runner's empty network namespace.
// It tunnels HTTPS to public addresses and refuses everything a job must not
// reach: loopback, private and link-local ranges, and tailnet addresses. Each job
// gets its own socket, which is how one job's limits stay apart from another's.
package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"sync"
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
	netip.MustParsePrefix("::/96"),          // IPv4-compatible, the older form of a mapped address.
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 can lead back to a private IPv4 address.
	netip.MustParsePrefix("64:ff9b:1::/48"), // So can local NAT64.
	netip.MustParsePrefix("2001::/32"),      // So can Teredo.
	netip.MustParsePrefix("2002::/16"),      // So can 6to4.
	netip.MustParsePrefix("fec0::/10"),      // Site-local, deprecated but still routable on a LAN.
}

// One proxy serves every job on a host, so one job must not be able to take it
// down: an out-of-memory kill would cut every other job's tunnels.
const maxRequest = 64 << 10 // bytes in a CONNECT request

const (
	// hostRate is what the proxy moves for all jobs together. It sits below the
	// uplink, so the proxy decides the shares and not TCP, which would divide the
	// link by tunnel and hand it to whichever job holds the most.
	hostRate = 64 << 20 // bytes a second
	// busyWindow is how recently a job moved bytes to count for a share. An idle
	// job's long poll takes nothing from the others.
	busyWindow = time.Second
	// burst is the credit a job keeps after a pause. Sleeps overshoot, and without
	// it a tunnel could never catch up to its share.
	burst = 50 * time.Millisecond
)

type limits struct {
	// tunnels is one job's limit. All jobs' tunnels stay well inside the proxy's
	// memory limit, at about 100 KiB each.
	tunnels int
	// closeWait is how long a far end may hold a tunnel after its job has left it.
	closeWait time.Duration
	rate      float64 // bytes a second for the host, shared by the jobs moving data
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

// Serve answers HTTP CONNECT until ctx ends, on one listener per job.
func Serve(ctx context.Context, listeners []net.Listener, dial Dial) error {
	return serve(ctx, listeners, dial, limits{tunnels: 256, closeWait: time.Minute, rate: hostRate})
}

// job is what one listener's connections share.
type job struct {
	slots chan struct{}
	host  *host
	moved time.Time // guarded by host.mu
	turn  time.Time // when its next bytes may go, guarded by host.mu
}

type host struct {
	rate float64
	mu   sync.Mutex
	jobs []*job
}

// pace holds n bytes until the job's share of the host's rate allows them. The
// share is even across the jobs moving data now, however many tunnels each holds.
func (j *job) pace(n int) {
	h := j.host
	h.mu.Lock()
	now := time.Now()
	j.moved = now
	busy := 0
	for _, other := range h.jobs {
		if now.Sub(other.moved) < busyWindow {
			busy++
		}
	}
	if earliest := now.Add(-burst); j.turn.Before(earliest) {
		j.turn = earliest
	}
	wait := j.turn.Sub(now)
	j.turn = j.turn.Add(time.Duration(float64(n) * float64(busy) / h.rate * float64(time.Second)))
	h.mu.Unlock()
	time.Sleep(wait) // a negative wait returns at once
}

// paced counts what a tunnel writes against its job's share.
type paced struct {
	io.Writer
	job *job
}

func (p paced) Write(data []byte) (int, error) {
	p.job.pace(len(data))
	return p.Writer.Write(data)
}

func serve(ctx context.Context, listeners []net.Listener, dial Dial, bound limits) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	shared := &host{rate: bound.rate}
	for range listeners {
		shared.jobs = append(shared.jobs, &job{slots: make(chan struct{}, bound.tunnels), host: shared})
	}
	failed := make(chan error, len(listeners))
	for i, listener := range listeners {
		stop := context.AfterFunc(ctx, func() { listener.Close() })
		defer stop()
		go func() { failed <- accept(ctx, listener, dial, bound, shared.jobs[i]) }()
	}
	var first error
	for range listeners {
		if err := <-failed; err != nil && first == nil {
			// The proxy restarts as a whole, so one bad socket ends the rest.
			first = err
			cancel()
		}
	}
	return first
}

// accept serves one job. Being past the limit costs that job alone.
func accept(ctx context.Context, listener net.Listener, dial Dial, bound limits, j *job) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case j.slots <- struct{}{}:
			go func() {
				defer func() { <-j.slots }()
				handle(ctx, conn, dial, bound.closeWait, j)
			}()
		default:
			reply(conn, http.StatusTooManyRequests)
			conn.Close()
		}
	}
}

func handle(ctx context.Context, conn net.Conn, dial Dial, closeWait time.Duration, j *job) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	limited := &io.LimitedReader{R: conn, N: maxRequest}
	reader := bufio.NewReader(limited)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	limited.N = math.MaxInt64
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
		_, _ = io.Copy(paced{upstream, j}, reader)
		if half, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
		_ = upstream.SetReadDeadline(time.Now().Add(closeWait))
		close(done)
	}()
	_, _ = io.Copy(paced{conn, j}, upstream)
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	}
	<-done
}

func reply(conn net.Conn, status int) {
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n\r\n", status, http.StatusText(status))
}
