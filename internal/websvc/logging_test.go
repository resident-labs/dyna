package websvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanceledRequestLogging(t *testing.T) {
	var methodLogs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&methodLogs)
	t.Cleanup(func() { log.SetOutput(oldOutput) })
	for _, canceled := range []bool{true, false} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			methodLogs.Reset()
			var accessLogs bytes.Buffer
			name := fmt.Sprintf("CancellationLogTest-%t", canceled)
			t.Cleanup(func() {
				transientLogMu.Lock()
				delete(transientLogLast, name)
				transientLogMu.Unlock()
			})
			s := &Server{o: Options{
				Calls: map[string]Call{name: func(context.Context, []json.RawMessage) (any, error) { return nil, context.Canceled }},
				Logf:  func(format string, args ...any) { fmt.Fprintf(&accessLogs, format, args...) },
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			r := httptest.NewRequest(http.MethodPost, "http://localhost/api", strings.NewReader("[]")).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://localhost")
			r.Header.Set("X-Dyna", "1")
			w := httptest.NewRecorder()
			s.logged("app", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.call(w, r, Route{App: "http://localhost"}, Principal{Kind: "local"}, name)
			})).ServeHTTP(w, r)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			if canceled {
				if methodLogs.Len() != 0 || accessLogs.Len() != 0 {
					t.Fatalf("canceled request logged: method=%q access=%q", methodLogs.String(), accessLogs.String())
				}
			} else if methodLogs.Len() == 0 || accessLogs.Len() == 0 {
				t.Fatalf("live request error lost: method=%q access=%q", methodLogs.String(), accessLogs.String())
			}
		})
	}
}
