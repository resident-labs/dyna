package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Pi's GitHub server reaches the engine's /github/ unchanged, with its streamed answer.
func TestGitHubRouteReachesTheEngine(t *testing.T) {
	route := modelRoute(transportFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/github/" || string(body) != `{"method":"tools/list"}` {
			t.Fatalf("the engine got %s %q", r.URL.Path, body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}, nil
	}))
	out := httptest.NewRecorder()
	route.ServeHTTP(out, httptest.NewRequest("POST", "http://127.0.0.1:7781/github/", strings.NewReader(`{"method":"tools/list"}`)))
	if out.Code != 200 || out.Body.String() != "data: {}\n\n" {
		t.Fatalf("%d %q", out.Code, out.Body.String())
	}
}
