package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCheckoutTruncatedTarReleasesLease(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var discarded bool
	var discardProfile, discardGen string

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/browser/checkout":
			w.Header().Set("Content-Type", "application/x-tar")
			w.Header().Set("X-Dyna-Browser-Profile", "work")
			w.Header().Set("X-Dyna-Browser-Generation", "7")
			// Write a tar header claiming 4096 bytes, then only 10 bytes and abort the stream.
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			_ = tw.WriteHeader(&tar.Header{Name: "Default/Cookies", Mode: 0600, Size: 4096})
			_, _ = w.Write(buf.Bytes())
			_, _ = w.Write([]byte("truncated!"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		case "/v1/browser/checkin":
			mu.Lock()
			discarded = r.URL.Query().Get("discard") == "1"
			discardProfile = r.URL.Query().Get("profile")
			discardGen = r.URL.Query().Get("generation")
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	client := NewClient(socket)
	dir := filepath.Join(t.TempDir(), "computer-shared")
	if _, err := client.Checkout(context.Background(), dir); err == nil {
		t.Fatal("expected error on truncated checkout tar")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("partial checkout dir not cleaned up: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !discarded || discardProfile != "work" || discardGen != "7" {
		t.Fatalf("expected discard checkin for work gen 7, got discarded=%v profile=%q gen=%q", discarded, discardProfile, discardGen)
	}
}
