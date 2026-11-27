package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/engine/agyexit"
)

// engineWith serves the real exit route on a unix socket, dialling out through dial.
func engineWith(t *testing.T, dial func(context.Context, string, string) (net.Conn, error)) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "e.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: agyexit.Handler(dial)}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return socket
}

func connect(t *testing.T, proxy *httptest.Server, line string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "%s HTTP/1.1\r\nHost: x\r\n\r\n", line)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	return response, conn, reader
}

func TestAgyExitCarriesAConnectThroughTheEngine(t *testing.T) {
	upstream, remote := net.Pipe()
	proxy := httptest.NewServer(agyExit(engineWith(t, func(context.Context, string, string) (net.Conn, error) { return upstream, nil })))
	defer proxy.Close()
	response, conn, reader := connect(t, proxy, "CONNECT accounts.google.com:443")
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	go fmt.Fprint(conn, "hello")
	got := make([]byte, 5)
	if _, err := io.ReadFull(remote, got); err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
	go fmt.Fprint(remote, "world")
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "world" {
		t.Fatalf("%q %v", got, err)
	}
	// The engine going away mid-tunnel closes agy's end; nothing is sent again.
	remote.Close()
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("the client stayed connected")
	}
}

func TestAgyExitPassesTheEnginesRefusalToAgy(t *testing.T) {
	proxy := httptest.NewServer(agyExit(engineWith(t, nil)))
	defer proxy.Close()
	response, _, _ := connect(t, proxy, "CONNECT example.com:443")
	if response.StatusCode != 403 || !strings.Contains(response.Status, "does not serve example.com") {
		t.Fatalf("%q", response.Status)
	}
}

func TestAgyExitIsA502WhileTheEngineIsAbsentAndRecovers(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "none.sock")
	proxy := httptest.NewServer(agyExit(socket))
	defer proxy.Close()
	response, _, _ := connect(t, proxy, "CONNECT accounts.google.com:443")
	if response.StatusCode != 502 || !strings.Contains(response.Status, "the agy exit is unreachable") {
		t.Fatalf("%q", response.Status)
	}
	// An engine back at the same socket serves the next CONNECT.
	upstream, _ := net.Pipe()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: agyexit.Handler(func(context.Context, string, string) (net.Conn, error) { return upstream, nil })}
	go server.Serve(listener)
	defer server.Close()
	if response, _, _ := connect(t, proxy, "CONNECT accounts.google.com:443"); response.StatusCode != 200 {
		t.Fatalf("after the engine came back: %q", response.Status)
	}
}

func TestAgyExitTakesOnlyConnect(t *testing.T) {
	proxy := httptest.NewServer(agyExit(filepath.Join(t.TempDir(), "none.sock")))
	defer proxy.Close()
	response, err := http.Get(proxy.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 405 {
		t.Fatalf("status %d", response.StatusCode)
	}
}
