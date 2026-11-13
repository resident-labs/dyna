package buildcache

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/internal/testutil"
)

func request(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

// sccache writes an entry under a key with folders it never made, and reads it back by the same key.
func TestAnEntryReadsBackWhole(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(Handler(dir))
	defer server.Close()
	if code, _ := request(t, http.MethodPut, server.URL+"/a/b/c/key", "entry"); code != http.StatusCreated {
		t.Fatalf("put: %d", code)
	}
	if code, body := request(t, http.MethodGet, server.URL+"/a/b/c/key", ""); code != http.StatusOK || body != "entry" {
		t.Fatalf("get: %d %q", code, body)
	}
	if code, _ := request(t, http.MethodGet, server.URL+"/a/b/c/other", ""); code != http.StatusNotFound {
		t.Fatalf("a missing entry answered %d", code)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "a", "b", "c"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "key" {
		t.Fatalf("the folder holds %v: %v", entries, err)
	}
}

func TestAWriteStaysInTheStore(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	server := httptest.NewServer(Handler(dir))
	defer server.Close()
	req, err := http.NewRequest(http.MethodPut, server.URL+"/x", strings.NewReader("entry"))
	if err != nil {
		t.Fatal(err)
	}
	req.URL.Path = "/../../escape"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := os.Stat(filepath.Join(root, "escape")); err == nil {
		t.Fatal("a write left the store")
	}
	if code, _ := request(t, http.MethodPut, server.URL+"/"+tempPrefix+"x", "entry"); code != http.StatusBadRequest {
		t.Fatalf("a write under a temporary name answered %d", code)
	}
}

func TestAReadMarksTheEntryUsed(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(Handler(dir))
	defer server.Close()
	request(t, http.MethodPut, server.URL+"/key", "entry")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "key"), old, old); err != nil {
		t.Fatal(err)
	}
	request(t, http.MethodGet, server.URL+"/key", "")
	info, err := os.Stat(filepath.Join(dir, "key"))
	if err != nil || time.Since(info.ModTime()) > time.Hour {
		t.Fatalf("the read left the entry at %v: %v", info.ModTime(), err)
	}
}

// Past its bound, the store loses the entries used longest ago, down to nine tenths, and the files of writes that never ended.
func TestPruneRemovesWhatWasUsedLongestAgo(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, size int, age time.Duration) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	write("a/oldest", 400, 4*time.Hour)
	write("b/older", 300, 3*time.Hour)
	write("c/newer", 200, 2*time.Hour)
	write("d/newest", 100, time.Hour)
	write("a/"+tempPrefix+"stale", 10, 2*time.Hour)
	write("a/"+tempPrefix+"writing", 10, time.Minute)
	if err := prune(context.Background(), dir, 1000); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a/oldest", "b/older", "c/newer", "d/newest", "a/" + tempPrefix + "writing"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("pruned %s within the bound: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "a", tempPrefix+"stale")); err == nil {
		t.Fatal("a stale write stayed")
	}
	if err := prune(context.Background(), dir, 900); err != nil {
		t.Fatal(err)
	}
	for name, kept := range map[string]bool{"a/oldest": false, "b/older": true, "c/newer": true, "d/newest": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != kept {
			t.Errorf("%s kept %v, want %v", name, err == nil, kept)
		}
	}
	if err := prune(context.Background(), dir, 500); err != nil {
		t.Fatal(err)
	}
	for name, kept := range map[string]bool{"b/older": false, "c/newer": true, "d/newest": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != kept {
			t.Errorf("%s kept %v, want %v", name, err == nil, kept)
		}
	}
}

func TestServeAnswersOnItsSocketForAnyUser(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "cache.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, filepath.Join(dir, "store"), socket) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	testutil.Eventually(t, time.Second, "world-writable cache socket", func() bool {
		info, err := os.Stat(socket)
		return err == nil && info.Mode().Perm() == 0o666
	})
}
