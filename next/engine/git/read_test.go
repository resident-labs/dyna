package git

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReadOnlyCarriesTheTokenAndKeepsItOut(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Link", `<https://api.github.com/next>; rel="next"`)
		_, _ = io.WriteString(w, "[]")
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	handler := ReadOnly(target, http.DefaultTransport, "secret-token")

	r := httptest.NewRequest(http.MethodGet, "http://api.github.com/repos/o/r/issues?per_page=100", nil)
	r.Header.Set("Authorization", "token placeholder")
	r.Header.Set("Cookie", "a=b")
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("If-None-Match", `"abc"`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK || w.Body.String() != "[]" {
		t.Fatalf("reply: %d %q", w.Code, w.Body)
	}
	if got.URL.Path != "/repos/o/r/issues" || got.URL.RawQuery != "per_page=100" {
		t.Fatalf("path: %s", got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer secret-token" {
		t.Fatalf("authorization: %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("Cookie") != "" || got.Header.Get("If-None-Match") != `"abc"` {
		t.Fatalf("headers: %v", got.Header)
	}
	if w.Header().Get("Link") == "" || w.Header().Get("ETag") == "" {
		t.Fatalf("a paging reply lost its headers: %v", w.Header())
	}
	if strings.Contains(w.Body.String(), "secret-token") {
		t.Fatal("token in reply")
	}
}

func TestReadOnlyRefusesEveryWrite(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	handler := ReadOnly(target, http.DefaultTransport, "secret-token")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range []string{"/graphql", "/repos/o/r/merges", "/repos/o/r/git/refs/heads/main"} {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(method, "http://api.github.com"+path, strings.NewReader("{}")))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d", method, path, w.Code)
			}
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://api.github.com/GraphQL?query=mutation", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("GraphQL by GET: %d", w.Code)
	}
	if called {
		t.Fatal("a write reached GitHub")
	}
}

func TestReadOnlyDoesNotFollowARedirect(t *testing.T) {
	var followed bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer elsewhere.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	w := httptest.NewRecorder()
	ReadOnly(target, http.DefaultTransport, "secret-token").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://api.github.com/repos/o/r/tarball", nil))
	if w.Code != http.StatusFound || followed {
		t.Fatalf("redirect: %d, followed %v", w.Code, followed)
	}
}

func TestReadOnlyWithoutAToken(t *testing.T) {
	target, _ := url.Parse("http://127.0.0.1:1")
	w := httptest.NewRecorder()
	ReadOnly(target, http.DefaultTransport, "").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://api.github.com/rate_limit", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: %d", w.Code)
	}
}
