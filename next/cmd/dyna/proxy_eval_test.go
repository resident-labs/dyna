//go:build dynaeval

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type evalRoundTripFunc func(*http.Request) (*http.Response, error)

func (f evalRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEvalTransportRecordsOnlyHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hashes")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sink := &evalSink{file: f, ordinals: map[string]uint64{}}
	bodies := []string{`{"tools": [ {"name":"secret"} ], "messages":"private"}`, `{"messages":"private"}`, `{"tools":[]}`}
	next := evalRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != bodies[0] {
			t.Fatalf("body changed: %s %v", body, err)
		}
		bodies = bodies[1:]
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})
	transport := evalTransport{next: next, sink: sink, session: "s"}
	for range 3 {
		req, _ := http.NewRequest("POST", "https://example.invalid", bytes.NewBufferString(bodies[0]))
		if _, err := transport.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	pass := evalTransport{next: evalRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}), sink: sink, session: "s"}
	bodyless, _ := http.NewRequest("GET", "https://example.invalid", nil)
	if _, err := pass.RoundTrip(bodyless); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for i, raw := range []string{`[ {"name":"secret"} ]`, "", "[]"} {
		var record evalRecord
		if err := dec.Decode(&record); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(raw))
		if record != (evalRecord{"s", "example.invalid", "", uint64(i + 1), hex.EncodeToString(hash[:])}) {
			t.Fatalf("bad record: %+v", record)
		}
	}
	var extra evalRecord
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected extra record: %+v, %v", extra, err)
	}
	if bytes.Contains(data, []byte("secret")) || bytes.Contains(data, []byte("private")) {
		t.Fatal("record leaked request data")
	}
	f.Close()
	req, _ := http.NewRequest("POST", "https://example.invalid", bytes.NewBufferString("{}"))
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("sink error did not fail closed")
	}
}
