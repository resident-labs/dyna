package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicRefusesEveryInternalRange(t *testing.T) {
	for address, want := range map[string]bool{
		"140.82.114.4":           true,
		"2606:50c0:8000::153":    true,
		"127.0.0.1":              false,
		"::1":                    false,
		"10.0.0.1":               false,
		"172.16.0.1":             false,
		"192.168.2.9":            false,
		"169.254.169.254":        false,
		"100.122.131.34":         false, // a tailnet peer
		"fd7a:115c:a1e0::1":      false, // Tailscale's IPv6 range
		"::ffff:192.168.2.1":     false,
		"::ffff:100.100.100.100": false,
		"0.0.0.0":                false,
		"224.0.0.1":              false,
		"240.0.0.1":              false,
		"fe80::1":                false,
		"64:ff9b::c0a8:201":      false,
		"2002:c0a8:201::":        false,
		"64:ff9b:1::c0a8:201":    false,
		"2001:0:c0a8:201::":      false,
		"::c0a8:201":             false,
		"fec0::1":                false,
	} {
		if got := Public(netip.MustParseAddr(address)); got != want {
			t.Errorf("Public(%s) = %v, want %v", address, got, want)
		}
	}
}

// exchange sends one request and returns the status line.
func exchange(t *testing.T, dial Dial, request string) (string, *bufio.Reader, net.Conn) {
	t.Helper()
	listener, err := net.Listen("unix", t.TempDir()+"/s")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, []net.Listener{listener}, dial) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return status, reader, conn
}

func TestServeTunnelsHTTPS(t *testing.T) {
	var target string
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		target = address
		near, far := net.Pipe()
		go func() {
			defer far.Close()
			line, _ := bufio.NewReader(far).ReadString('\n')
			_, _ = io.WriteString(far, "echo "+line)
		}()
		return near, nil
	}
	status, reader, conn := exchange(t, dial, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n")
	if !strings.Contains(status, "200") {
		t.Fatalf("status %q", status)
	}
	if _, err := reader.ReadString('\n'); err != nil { // the blank line ending the headers
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "hello\n"); err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadString('\n')
	if err != nil || got != "echo hello\n" || target != "github.com:443" {
		t.Fatalf("got %q, target %q, error %v", got, target, err)
	}
}

func TestServeRefusesPortsMethodsAndPrivateAddresses(t *testing.T) {
	for name, request := range map[string]string{
		"ssh port":   "CONNECT github.com:22 HTTP/1.1\r\nHost: github.com:22\r\n\r\n",
		"plain http": "GET http://github.com/ HTTP/1.1\r\nHost: github.com\r\n\r\n",
		"loopback":   "CONNECT 127.0.0.1:443 HTTP/1.1\r\nHost: 127.0.0.1:443\r\n\r\n",
		"name":       "CONNECT localhost:443 HTTP/1.1\r\nHost: localhost:443\r\n\r\n", // checked after it resolves
		"lan":        "CONNECT 192.168.2.1:443 HTTP/1.1\r\nHost: 192.168.2.1:443\r\n\r\n",
		"tailnet":    "CONNECT 100.122.131.34:443 HTTP/1.1\r\nHost: 100.122.131.34:443\r\n\r\n",
		"metadata":   "CONNECT 169.254.169.254:443 HTTP/1.1\r\nHost: 169.254.169.254:443\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			status, _, _ := exchange(t, PublicDial(), request)
			if !strings.Contains(status, "403") {
				t.Fatalf("status %q, want 403", status)
			}
		})
	}
}

func TestServeReportsAFailedDial(t *testing.T) {
	dial := func(context.Context, string, string) (net.Conn, error) { return nil, io.ErrClosedPipe }
	status, _, _ := exchange(t, dial, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n")
	if !strings.Contains(status, "502") {
		t.Fatalf("status %q", status)
	}
}

// startJobs runs the proxy on one socket per job and returns a way to connect to each.
func startJobs(t *testing.T, jobs int, dial Dial, bound limits) []func() net.Conn {
	t.Helper()
	var listeners []net.Listener
	var connects []func() net.Conn
	for range jobs {
		listener, err := net.Listen("unix", t.TempDir()+"/s")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
		connects = append(connects, func() net.Conn {
			conn, err := net.Dial("unix", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close() })
			return conn
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listeners, dial, bound) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return connects
}

// start runs the proxy for one job and returns a way to connect to it.
func start(t *testing.T, dial Dial, bound limits) func() net.Conn {
	t.Helper()
	return startJobs(t, 1, dial, bound)[0]
}

const connect443 = "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n"

func TestOneJobPastItsTunnelsCostsOnlyThatJob(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	dialing := make(chan struct{}, 4)
	jobs := startJobs(t, 2, func(context.Context, string, string) (net.Conn, error) {
		dialing <- struct{}{}
		<-hold
		return nil, io.ErrClosedPipe
	}, limits{tunnels: 2, closeWait: time.Minute, rate: 1 << 30})
	for range 2 {
		if _, err := io.WriteString(jobs[0](), connect443); err != nil {
			t.Fatal(err)
		}
		<-dialing
	}
	third := jobs[0]()
	_ = third.SetReadDeadline(time.Now().Add(5 * time.Second))
	if status, err := bufio.NewReader(third).ReadString('\n'); err != nil || !strings.Contains(status, "429") {
		t.Fatalf("a tunnel past the limit got %q, %v", status, err)
	}
	// The other job's socket is still open for business.
	if _, err := io.WriteString(jobs[1](), connect443); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dialing:
	case <-time.After(5 * time.Second):
		t.Fatal("one job's tunnels starved another")
	}
}

func TestAClosedTunnelFreesItsSlot(t *testing.T) {
	connect := start(t, func(context.Context, string, string) (net.Conn, error) {
		near, far := net.Pipe()
		go far.Close()
		return near, nil
	}, limits{tunnels: 1, closeWait: time.Millisecond, rate: 1 << 30})
	for range 3 {
		conn := connect()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, connect443); err != nil {
			t.Fatal(err)
		}
		if status, err := bufio.NewReader(conn).ReadString('\n'); err != nil || !strings.Contains(status, "200") {
			t.Fatalf("%q %v", status, err)
		}
		conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFailingSocketEndsTheProxySoItRestarts(t *testing.T) {
	listener, err := net.Listen("unix", t.TempDir()+"/s")
	if err != nil {
		t.Fatal(err)
	}
	broken, err := net.Listen("unix", t.TempDir()+"/s")
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	done := make(chan error, 1)
	go func() { done <- serve(context.Background(), []net.Listener{listener, broken}, nil, limits{tunnels: 1}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a dead socket went unreported")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy kept running with a dead socket")
	}
}

// flood is a far end that sends as fast as it is read.
func flood(context.Context, string, string) (net.Conn, error) {
	near, far := net.Pipe()
	go func() {
		defer far.Close()
		chunk := make([]byte, 32<<10)
		for {
			if _, err := far.Write(chunk); err != nil {
				return
			}
		}
	}()
	return near, nil
}

// download opens tunnels on one job and counts what comes back.
func download(t *testing.T, connect func() net.Conn, tunnels int, total *atomic.Int64) {
	t.Helper()
	for range tunnels {
		conn := connect()
		if _, err := io.WriteString(conn, connect443); err != nil {
			t.Fatal(err)
		}
		go func() {
			buffer := make([]byte, 64<<10)
			for {
				n, err := conn.Read(buffer)
				total.Add(int64(n))
				if err != nil {
					return
				}
			}
		}()
	}
}

const testRate = 64 << 20

func TestJobsShareTheRateEvenlyHoweverManyTunnelsTheyHold(t *testing.T) {
	jobs := startJobs(t, 2, flood, limits{tunnels: 64, closeWait: time.Millisecond, rate: testRate})
	var hog, quiet atomic.Int64
	download(t, jobs[0], 32, &hog)
	download(t, jobs[1], 1, &quiet)
	time.Sleep(1200 * time.Millisecond) // long enough for both to count as busy
	hogStart, quietStart := hog.Load(), quiet.Load()
	time.Sleep(time.Second)
	gotHog, gotQuiet := float64(hog.Load()-hogStart), float64(quiet.Load()-quietStart)
	if gotQuiet < gotHog/2 {
		t.Fatalf("a hog with 32 tunnels took %.0f MiB/s against %.0f MiB/s for one tunnel", gotHog/(1<<20), gotQuiet/(1<<20))
	}
	if total := gotHog + gotQuiet; total > 1.2*testRate {
		t.Fatalf("the host's rate was exceeded: %.0f MiB/s", total/(1<<20))
	}
}

func TestAnIdleJobTakesNoShare(t *testing.T) {
	jobs := startJobs(t, 3, flood, limits{tunnels: 64, closeWait: time.Millisecond, rate: testRate})
	// Open tunnels that move nothing, like a runner's long poll.
	idle := jobs[1]()
	if _, err := io.WriteString(idle, "CONNECT github.com:443 HTTP/1.1\r\n"); err != nil {
		t.Fatal(err)
	}
	var busy atomic.Int64
	download(t, jobs[0], 4, &busy)
	time.Sleep(200 * time.Millisecond)
	start := busy.Load()
	time.Sleep(time.Second)
	if got := busy.Load() - start; got < testRate/2 {
		t.Fatalf("a lone busy job got %d MiB/s of %d", got>>20, testRate>>20)
	}
}

func TestServeDropsAnOversizedRequest(t *testing.T) {
	connect := start(t, func(context.Context, string, string) (net.Conn, error) {
		t.Error("an oversized request reached the dialer")
		return nil, io.ErrClosedPipe
	}, limits{tunnels: 2, closeWait: time.Minute, rate: testRate})
	conn := connect()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	go func() {
		// Headers with no end. The proxy must close before they do.
		_, _ = io.WriteString(conn, "CONNECT github.com:443 HTTP/1.1\r\n")
		for {
			if _, err := io.WriteString(conn, "X-Pad: "+strings.Repeat("a", 1024)+"\r\n"); err != nil {
				return
			}
		}
	}()
	if _, err := io.Copy(io.Discard, conn); err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("the proxy kept reading: %v", err)
	}
}

func TestAFarEndThatNeverClosesCannotKeepATunnel(t *testing.T) {
	far, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer far.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := far.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	released := make(chan struct{})
	connect := start(t, func(ctx context.Context, network, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, far.Addr().String())
		return watched{conn, released}, err
	}, limits{tunnels: 2, closeWait: 10 * time.Millisecond, rate: testRate})
	conn := connect().(*net.UnixConn)
	if _, err := io.WriteString(conn, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(line, "200") {
		t.Fatalf("%q %v", line, err)
	}
	held := <-accepted // never closes
	defer held.Close()
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel outlived its job")
	}
}

// watched reports when the proxy closes its upstream connection.
type watched struct {
	net.Conn
	closed chan struct{}
}

func (w watched) Close() error {
	close(w.closed)
	return w.Conn.Close()
}
