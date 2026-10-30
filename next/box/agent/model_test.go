package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
