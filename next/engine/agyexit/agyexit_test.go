package agyexit

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// post sends the box's POST to the handler and returns the status line and the connection.
func post(t *testing.T, server *httptest.Server, method, target string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "%s /agy-exit/%s HTTP/1.1\r\nHost: dyna\r\nContent-Length: 0\r\n\r\n", method, target)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	return response, conn, reader
}

func TestExitServesOnlyGoogleHostsOnPort443(t *testing.T) {
	var dialed []string
	server := httptest.NewServer(Handler(func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, errors.New("down")
	}))
	defer server.Close()
	for _, c := range []struct {
		method, target string
		status         int
		dials          bool
	}{
		{"POST", "daily-cloudcode-pa.googleapis.com:443", 502, true},
		{"POST", "accounts.google.com:443", 502, true},
		{"POST", "ssl.gstatic.com:443", 502, true},
		{"POST", "lh3.googleusercontent.com:443", 502, true},
		{"POST", "example.com:443", 403, false},
		{"POST", "evilgoogleapis.com:443", 403, false},
		{"POST", "googleapis.com.evil.com:443", 403, false},
		{"POST", "accounts.google.com:80", 403, false},
		{"POST", "accounts.google.com", 403, false},
		{"POST", "142.250.1.1:443", 403, false},
		{"POST", "Accounts.Google.com:443", 403, false},
		{"POST", "a..google.com:443", 403, false},
		{"GET", "accounts.google.com:443", 405, false},
	} {
		dialed = nil
		response, _, _ := post(t, server, c.method, c.target)
		if response.StatusCode != c.status || response.Header.Get("X-Dyna-Exit") == "" {
			t.Errorf("%s %s: %d %q", c.method, c.target, response.StatusCode, response.Header.Get("X-Dyna-Exit"))
		}
		if (len(dialed) == 1) != c.dials || len(dialed) > 1 {
			t.Errorf("%s %s dialed %v", c.method, c.target, dialed)
		}
	}
}

func TestExitJoinsTheTwoEndsAndLogsNoBytes(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(io.Discard)
	upstream, remote := net.Pipe()
	server := httptest.NewServer(Handler(func(context.Context, string, string) (net.Conn, error) { return upstream, nil }))
	defer server.Close()
	response, conn, reader := post(t, server, "POST", "accounts.google.com:443")
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	const sentinel = "SENTINEL-TLS-BYTES"
	go fmt.Fprint(conn, sentinel)
	got := make([]byte, len(sentinel))
	if _, err := io.ReadFull(remote, got); err != nil || string(got) != sentinel {
		t.Fatalf("up: %q %v", got, err)
	}
	go fmt.Fprint(remote, strings.ToLower(sentinel))
	back := make([]byte, len(sentinel))
	if _, err := io.ReadFull(reader, back); err != nil || string(back) != strings.ToLower(sentinel) {
		t.Fatalf("down: %q %v", back, err)
	}
	remote.Close()
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("the tunnel stayed open after the far end closed")
	}
	if !strings.Contains(logged.String(), "agy exit: accounts.google.com:443") || strings.Contains(strings.ToLower(logged.String()), "sentinel") {
		t.Fatalf("log %q", logged.String())
	}
}
