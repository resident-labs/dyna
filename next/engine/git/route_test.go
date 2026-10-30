package git

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type fakeAccess struct {
	allowed bool
}

func (a fakeAccess) Allowed(context.Context, contract.SessionID, contract.RepoName) (bool, error) {
	return a.allowed, nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRejectsOtherRooms(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	called := false
	transport := roundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", fakeAccess{}).ServeHTTP(w, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
	if w.Code != http.StatusForbidden || called {
		t.Fatalf("status %d, called %v", w.Code, called)
	}
}

func TestRejectsUnownedPushBeforeUpstream(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	called := false
	transport := roundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/main\x00report-status\n"
	body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", fakeAccess{allowed: true}).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	if called || !strings.Contains(w.Body.String(), "ng refs/heads/main not owned by this session") {
		t.Fatalf("upstream called %v, response %q", called, w.Body.String())
	}
}

func TestForwardsOwnedPushAndReplacesCredential(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/v2/ses_1\x00report-status\n"
	body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		got, err := io.ReadAll(r.Body)
		user, pass, ok := r.BasicAuth()
		if err != nil || string(got) != body || !ok || user != "x-access-token" || pass != "secret" || r.URL.Path != "/owner/repo.git/git-receive-pack" {
			t.Fatalf("bad forwarded request: %q %s %v", got, r.URL.Path, err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	req := httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body))
	req.Header.Set("Authorization", "bad")
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", fakeAccess{allowed: true}).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}
