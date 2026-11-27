package agent

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// fakeEngine is the engine's agy exit on a unix socket: it records each target it is asked for, serves a Google host on 443 by joining
// the client to upstream, and refuses anything else with the reason in X-Dyna-Exit, as the real route does.
type fakeEngine struct {
	mu       sync.Mutex
	targets  []string
	upstream net.Conn
}

func (f *fakeEngine) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.targets...)
}

func startEngine(t *testing.T, socket string, f *fakeEngine) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := strings.TrimPrefix(r.URL.Path, contract.AgyExitPath)
		f.mu.Lock()
		f.targets = append(f.targets, target)
		f.mu.Unlock()
		host, port, _ := net.SplitHostPort(target)
		if normal, ok := contract.AgyExitHost(host); !ok || normal != host || port != "443" {
			w.Header().Set("X-Dyna-Exit", "the agy exit does not serve "+target)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(f.upstream, conn); done <- struct{}{} }()
		go func() { _, _ = io.Copy(conn, f.upstream); done <- struct{}{} }()
		<-done
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
}

func connect(t *testing.T, proxy *httptest.Server, target string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil {
		t.Fatal(err)
	}
	return response, conn, reader
}

// echo is a plain TCP server that sends back what it reads, the target of a direct CONNECT.
func echo(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	return listener.Addr().String()
}

func TestAgyProxyCarriesAGoogleConnectThroughTheEngineOnly(t *testing.T) {
	upstream, remote := net.Pipe()
	socket := filepath.Join(t.TempDir(), "e.sock")
	engine := &fakeEngine{upstream: upstream}
	startEngine(t, socket, engine)
	proxy := httptest.NewServer(agyProxy(socket))
	defer proxy.Close()
	// An upper-case name with a trailing dot is Google's all the same, and reaches the engine in its normal form.
	response, conn, reader := connect(t, proxy, "ACCOUNTS.Google.com.:443")
	if response.StatusCode != 200 || len(engine.asked()) != 1 || engine.asked()[0] != "accounts.google.com:443" {
		t.Fatalf("status %d, engine asked %v", response.StatusCode, engine.asked())
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

func TestAgyProxyDialsOtherHostsDirectlyAndNeverTouchesTheEngine(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "e.sock")
	engine := &fakeEngine{}
	startEngine(t, socket, engine)
	proxy := httptest.NewServer(agyProxy(socket))
	defer proxy.Close()
	response, conn, reader := connect(t, proxy, echo(t))
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	fmt.Fprint(conn, "ping")
	got := make([]byte, 4)
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "ping" {
		t.Fatalf("%q %v", got, err)
	}
	if asked := engine.asked(); len(asked) != 0 {
		t.Fatalf("the engine was asked for %v", asked)
	}
	// A target that cannot be reached is a 502 of this box's own, not the engine's.
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := dead.Addr().String()
	dead.Close()
	if response, _, _ := connect(t, proxy, addr); response.StatusCode != 502 || len(engine.asked()) != 0 {
		t.Fatalf("status %d, engine asked %v", response.StatusCode, engine.asked())
	}
}

func TestAgyProxyForwardsPlainHTTPDirectly(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "e.sock")
	engine := &fakeEngine{}
	startEngine(t, socket, engine)
	var seen http.Header
	var seenURI string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenURI = r.Header.Clone(), r.RequestURI
		w.Header().Set("X-Origin", "yes")
		fmt.Fprint(w, "from the origin")
	}))
	defer origin.Close()
	proxy := httptest.NewServer(agyProxy(socket))
	defer proxy.Close()
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s/path?q=1 HTTP/1.1\r\nHost: %s\r\nConnection: close, X-Hop\r\nX-Hop: gone\r\nX-Keep: kept\r\n\r\n", origin.URL, origin.Listener.Addr())
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(body) != "from the origin" || response.Header.Get("X-Origin") != "yes" {
		t.Fatalf("%d %q %v", response.StatusCode, body, response.Header)
	}
	if seenURI != "/path?q=1" || seen.Get("X-Keep") != "kept" || seen.Get("X-Hop") != "" {
		t.Fatalf("the origin saw %q %v", seenURI, seen)
	}
	if asked := engine.asked(); len(asked) != 0 {
		t.Fatalf("the engine was asked for %v", asked)
	}
}

func TestAgyProxyRefusesPlainHTTPToGoogleAndToItself(t *testing.T) {
	proxy := httptest.NewServer(agyProxy(filepath.Join(t.TempDir(), "none.sock")))
	defer proxy.Close()
	for _, target := range []string{"http://accounts.google.com/x", "http://" + contract.AgyExitAddr + "/x"} {
		conn, _ := net.Dial("tcp", proxy.Listener.Addr().String())
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\n\r\n", target)
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil || response.StatusCode != 403 {
			t.Errorf("%s: %v %v", target, response, err)
		}
	}
	// A request that is not absolute is not a proxy request.
	response, err := http.Get(proxy.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestAgyProxyPassesTheEnginesRefusalForAGoogleHost(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "e.sock")
	engine := &fakeEngine{}
	startEngine(t, socket, engine)
	proxy := httptest.NewServer(agyProxy(socket))
	defer proxy.Close()
	response, _, _ := connect(t, proxy, "accounts.google.com:80")
	if response.StatusCode != 403 || !strings.Contains(response.Status, "does not serve accounts.google.com:80") {
		t.Fatalf("%q", response.Status)
	}
}

// With the engine away only the Google hosts fail, once each; every other connection and request goes on, and the next Google CONNECT
// is served once the engine is back.
func TestAgyProxyWithTheEngineDownFailsOnlyGoogleHosts(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "e.sock")
	proxy := httptest.NewServer(agyProxy(socket))
	defer proxy.Close()
	if response, _, _ := connect(t, proxy, "accounts.google.com:443"); response.StatusCode != 502 || !strings.Contains(response.Status, "the agy exit is unreachable") {
		t.Fatalf("%q", response.Status)
	}
	response, conn, reader := connect(t, proxy, echo(t))
	if response.StatusCode != 200 {
		t.Fatalf("a direct connection with the engine down: %d", response.StatusCode)
	}
	fmt.Fprint(conn, "up")
	if got := make([]byte, 2); func() bool { _, err := io.ReadFull(reader, got); return err != nil || string(got) != "up" }() {
		t.Fatal("the direct connection carried nothing")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer origin.Close()
	plain, _ := net.Dial("tcp", proxy.Listener.Addr().String())
	defer plain.Close()
	fmt.Fprintf(plain, "GET %s/ HTTP/1.1\r\nHost: %s\r\n\r\n", origin.URL, origin.Listener.Addr())
	if response, err := http.ReadResponse(bufio.NewReader(plain), nil); err != nil || response.StatusCode != 200 {
		t.Fatalf("plain HTTP with the engine down: %v %v", response, err)
	}
	upstream, _ := net.Pipe()
	startEngine(t, socket, &fakeEngine{upstream: upstream})
	if response, _, _ := connect(t, proxy, "accounts.google.com:443"); response.StatusCode != 200 {
		t.Fatalf("after the engine came back: %q", response.Status)
	}
}
