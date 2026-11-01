package egress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
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
	go func() { done <- Serve(ctx, listener, dial) }()
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
