package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRunningSumIsTheChecksumOfThisBinary(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	got, err := runningSum()
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("sum=%s, want %x", got, want)
	}
}

func TestChannelReportsBinaryOnEveryConnection(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query().Get("binary"))
		if len(seen) == 2 {
			_, _ = w.Write([]byte("{\"type\":\"computer.watch.stop\",\"body\":{}}\n"))
		}
	}))
	defer server.Close()
	c := &Client{http: server.Client()}
	c.http.Transport = rewriteTransport{base: server.URL, next: c.http.Transport}
	done := errors.New("second connection reached the handler")
	err := c.Channel(context.Background(), "abc", func(contract.BoxMessage) error { return done }, func(bool) {})
	if !errors.Is(err, done) || len(seen) != 2 || seen[0] != "abc" || seen[1] != "abc" {
		t.Fatalf("err=%v seen=%v", err, seen)
	}
}
