package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/box/browser"
	"github.com/resident-labs/dyna/contract"
)

func TestBrowserCookieSyncClient(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "engine.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var step int
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/browser/sync" {
			http.NotFound(w, r)
			return
		}
		step++
		switch step {
		case 1:
			if r.Method != http.MethodGet {
				t.Errorf("step 1 method = %s, want GET", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case 2:
			if r.Method != http.MethodGet || r.URL.Query().Get("since") != "3" {
				t.Errorf("step 2 = %s %s", r.Method, r.URL.RawQuery)
			}
			c, _ := browser.ParseCookie([]byte(`{"name":"sid","value":"v1","domain":"example.com","path":"/","expires":-1,"session":true}`))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(browser.SyncResponse{Profile: "personal", Version: 4, Upserts: []browser.Cookie{c}})
		case 3:
			if r.Method != http.MethodPost {
				t.Errorf("step 3 method = %s, want POST", r.Method)
			}
			var got browser.SyncRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decode req: %v", err)
			}
			if got.Profile != "personal" || got.BaseVersion != 4 || len(got.Upserts) != 1 {
				t.Errorf("unexpected sync req: profile=%q base=%d upserts=%d", got.Profile, got.BaseVersion, len(got.Upserts))
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(browser.SyncResponse{Profile: "personal", Version: 5})
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(contract.Error{Code: contract.ErrInvalid, Message: "cookie jar limit exceeded"})
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	client := NewClient(socket)
	ctx := context.Background()

	// 204 -> active=false, err=nil.
	if _, active, err := client.PullCookies(ctx, 0); err != nil || active {
		t.Fatalf("pull 204: active=%v err=%v", active, err)
	}
	// GET with since -> active=true.
	resp, active, err := client.PullCookies(ctx, 3)
	if err != nil || !active || resp.Profile != "personal" || resp.Version != 4 || len(resp.Upserts) != 1 {
		t.Fatalf("pull 200: active=%v resp=%+v err=%v", active, resp, err)
	}
	// POST sync -> version 5.
	c, _ := browser.ParseCookie([]byte(`{"name":"a","value":"b","domain":"example.com","path":"/","expires":-1,"session":true}`))
	resp, active, err = client.SyncCookies(ctx, browser.SyncRequest{Profile: "personal", BaseVersion: 4, Upserts: []browser.Cookie{c}})
	if err != nil || !active || resp.Version != 5 {
		t.Fatalf("sync 200: active=%v ver=%d err=%v", active, resp.Version, err)
	}
	// Error status returns engine message.
	if _, _, err := client.SyncCookies(ctx, browser.SyncRequest{Profile: "personal", BaseVersion: 5}); err == nil || err.Error() != "cookie jar limit exceeded" {
		t.Fatalf("sync 413 err = %v", err)
	}
}
