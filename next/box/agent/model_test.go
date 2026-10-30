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
