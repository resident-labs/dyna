package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type trackedModelBody struct {
	io.Reader
	closes int
}

func (b *trackedModelBody) Close() error { b.closes++; return nil }

type failedModelReader struct{ err error }

func (r failedModelReader) Read([]byte) (int, error) { return 0, r.err }

func TestHoldingLengthIsOnlyAHint(t *testing.T) {
	for _, hint := range []int64{-1, 0, 1, 4096, (16 << 20) - 512, 16 << 20, math.MaxInt64} {
		t.Run(fmt.Sprint(hint), func(t *testing.T) {
			payload := bytes.Repeat([]byte("payload"), 1024)
			body := &trackedModelBody{Reader: bytes.NewReader(payload)}
			req := httptest.NewRequest("POST", "http://dyna/model/messages", nil)
			req.Body, req.ContentLength = body, hint
			attempts := 0
			resp, err := (holding{wait: time.Second, next: transportFunc(func(r *http.Request) (*http.Response, error) {
				attempts++
				got, err := io.ReadAll(r.Body)
				r.Body.Close()
				if err != nil || !bytes.Equal(got, payload) || r.ContentLength != int64(len(payload)) {
					t.Fatalf("changed body: %d, %v", len(got), err)
				}
				if attempts == 1 {
					return nil, io.EOF
				}
				return &http.Response{Body: http.NoBody}, nil
			})}).RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if body.closes != 1 || attempts != 2 {
				t.Fatalf("closes=%d attempts=%d", body.closes, attempts)
			}
		})
	}
}

func TestHoldingReadErrorClosesWithoutSending(t *testing.T) {
	sentinel := errors.New("read failed")
	for _, hint := range []int64{-1, 8} {
		body := &trackedModelBody{Reader: io.MultiReader(bytes.NewReader([]byte("partial")), failedModelReader{sentinel})}
		req := httptest.NewRequest("POST", "http://dyna/model/messages", nil)
		req.Body, req.ContentLength = body, hint
		_, err := (holding{next: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("sent partial request"); return nil, nil })}).RoundTrip(req)
		if err != sentinel || body.closes != 1 {
			t.Fatalf("err=%v closes=%d", err, body.closes)
		}
	}
}

// Measures the real buffering path, including request/context setup, but not HTTP or replay.
func BenchmarkHoldingBuffer(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			payload := bytes.Repeat([]byte("x"), size)
			h := holding{wait: time.Second, next: transportFunc(func(r *http.Request) (*http.Response, error) {
				r.Body.Close()
				return &http.Response{Body: http.NoBody}, nil
			})}
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				req := httptest.NewRequest("POST", "http://dyna/model/messages", bytes.NewReader(payload))
				resp, err := h.RoundTrip(req)
				if err != nil {
					b.Fatal(err)
				}
				resp.Body.Close()
			}
		})
	}
}
