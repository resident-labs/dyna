package agent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type discardResponse struct {
	header http.Header
	size   int64
}

func (w *discardResponse) Header() http.Header         { return w.header }
func (w *discardResponse) WriteHeader(int)             {}
func (w *discardResponse) Write(p []byte) (int, error) { w.size += int64(len(p)); return len(p), nil }

func TestGitRouteStreamsLargeBodies(t *testing.T) {
	const size = outgoingLimit + 1
	route := modelRoute(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/git/owner/repo.git/git-receive-pack" {
			t.Fatal(r.URL.Path)
		}
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil || n != size {
			t.Fatalf("request: %d %v", n, err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(io.LimitReader(zeroReader{}, size))}, nil
	}))
	req := httptest.NewRequest("POST", "http://localhost/git/owner/repo.git/git-receive-pack", io.LimitReader(zeroReader{}, size))
	out := &discardResponse{header: make(http.Header)}
	route.ServeHTTP(out, req)
	if out.size != size {
		t.Fatalf("response: %d", out.size)
	}
}

func TestModelRouteForwardsRequestAndResponse(t *testing.T) {
	route := modelRoute(transportFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Method != "POST" || r.URL.RequestURI() != "/model/v1/messages?x=1" || r.Header.Get("X-Test") != "value" || string(body) != "payload" {
			t.Fatalf("request changed: %s %s %q %q", r.Method, r.URL.RequestURI(), r.Header.Get("X-Test"), body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: ok\n\n"))}, nil
	}))
	req := httptest.NewRequest("POST", "http://localhost/v1/messages?x=1", strings.NewReader("payload"))
	req.Header.Set("X-Test", "value")
	out := httptest.NewRecorder()
	route.ServeHTTP(out, req)
	if out.Code != 200 || out.Body.String() != "data: ok\n\n" || !out.Flushed {
		t.Fatalf("response changed: %d %q flushed=%v", out.Code, out.Body.String(), out.Flushed)
	}
}

// echoEngine answers on a Unix socket like the engine's forwarded socket.
func echoEngine(t *testing.T, socket string) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Error(err)
		return
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(w, r.Body) })}
	t.Cleanup(func() { server.Close() })
	go server.Serve(listener)
}

// An engine restart leaves a dead socket file on the host and then none, until the new master binds a new one. A request in
// that gap reached nothing, so it waits rather than spending Pi's retries on a 502.
func TestModelRouteWaitsForTheEngineSocket(t *testing.T) {
	dead := func(socket string) {
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		listener.(*net.UnixListener).SetUnlinkOnClose(false)
		listener.Close()
	}
	for name, before := range map[string]func(string){"missing": func(string) {}, "dead file": dead} {
		t.Run(name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "engine.sock")
			before(socket)
			route := ModelRoute(socket)
			timer := time.AfterFunc(300*time.Millisecond, func() {
				os.Remove(socket)
				echoEngine(t, socket)
			})
			defer timer.Stop()
			out := httptest.NewRecorder()
			route.ServeHTTP(out, httptest.NewRequest("POST", "http://localhost/v1/messages", strings.NewReader("payload")))
			if out.Code != 200 || out.Body.String() != "payload" {
				t.Fatalf("response %d %q", out.Code, out.Body.String())
			}
		})
	}
}

func TestModelRouteGivesUpOnAHostThatStaysGone(t *testing.T) {
	saved := socketWait
	socketWait = 50 * time.Millisecond
	defer func() { socketWait = saved }()
	out := httptest.NewRecorder()
	route := ModelRoute(filepath.Join(t.TempDir(), "engine.sock"))
	route.ServeHTTP(out, httptest.NewRequest("POST", "http://localhost/v1/messages", strings.NewReader("payload")))
	if out.Code != http.StatusBadGateway {
		t.Fatalf("response %d", out.Code)
	}
}

func TestModelRouteStopsWaitingWithItsRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	req := httptest.NewRequest("POST", "http://localhost/v1/messages", strings.NewReader("payload")).WithContext(ctx)
	done := make(chan struct{})
	route := ModelRoute(filepath.Join(t.TempDir(), "engine.sock"))
	go func() {
		defer close(done)
		route.ServeHTTP(httptest.NewRecorder(), req)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled request kept waiting")
	}
}
