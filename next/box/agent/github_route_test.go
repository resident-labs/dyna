package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// Pi's GitHub server reaches the engine's /github/, and agy's its /github-agy/, each unchanged with its streamed answer.
func TestGitHubRoutesReachTheEngine(t *testing.T) {
	for _, path := range []string{"/github/", contract.GitHubAgyPath} {
		route := modelRoute(transportFunc(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			if r.URL.Path != path || string(body) != `{"method":"tools/list"}` {
				t.Fatalf("the engine got %s %q for %s", r.URL.Path, body, path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}, nil
		}))
		out := httptest.NewRecorder()
		route.ServeHTTP(out, httptest.NewRequest("POST", "http://127.0.0.1:7781"+path, strings.NewReader(`{"method":"tools/list"}`)))
		if out.Code != 200 || out.Body.String() != "data: {}\n\n" {
			t.Fatalf("%s: %d %q", path, out.Code, out.Body.String())
		}
	}
}
