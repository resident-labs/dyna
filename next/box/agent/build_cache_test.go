package agent

import (
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

// sccache's endpoint names the route without a slash, and every key it adds starts with one, so a request reaches the cache
// with the key alone.
func TestBuildCacheRouteCarriesAKeyToTheSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "dyna-bc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "cache.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 1)
	go http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen <- r.Method + " " + r.URL.Path + " " + string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer listener.Close()
	mux := http.NewServeMux()
	mux.Handle("/build-cache/", http.StripPrefix("/build-cache", BuildCacheRoute(socket)))
	server := httptest.NewServer(mux)
	defer server.Close()
	req, err := http.NewRequest(http.MethodPut, server.URL+"/build-cache/6/2/a/key", strings.NewReader("entry"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := <-seen; got != "PUT /6/2/a/key entry" {
		t.Fatalf("the cache saw %q", got)
	}
}

// A box on a host whose cache is down still builds, so the route answers at once and sccache counts a miss.
func TestBuildCacheRouteFailsAtOnceWithoutTheSocket(t *testing.T) {
	server := httptest.NewServer(BuildCacheRoute(filepath.Join(t.TempDir(), "missing.sock")))
	defer server.Close()
	start := time.Now()
	resp, err := http.Get(server.URL + "/key")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || time.Since(start) > 5*time.Second {
		t.Fatalf("status %d after %v", resp.StatusCode, time.Since(start))
	}
}
