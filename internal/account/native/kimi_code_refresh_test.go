package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKimiCodeRefreshActiveCheckpointsExpiredLogin(t *testing.T) {
	for _, failCheckpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "checkpoint-failure"}[failCheckpoint], func(t *testing.T) {
			root := t.TempDir()
			config := []byte(`default_model = "kimi-code/k3"
[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"
`)
			token, _ := json.Marshal(kimiCodeToken{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix(), TokenType: "Bearer", ExpiresIn: 3600})
			if err := os.MkdirAll(filepath.Join(root, "credentials"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "config.toml"), config, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "credentials", "kimi-code.json"), token, 0600); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1900000000, 0).UTC()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != "17e5f671-d194-4dfb-9706-5516cb48c098" {
					t.Errorf("unexpected refresh form")
				}
				_ = json.NewEncoder(w).Encode(struct {
					AccessToken  string `json:"access_token"`
					RefreshToken string `json:"refresh_token"`
					ExpiresIn    int64  `json:"expires_in"`
					TokenType    string `json:"token_type"`
				}{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 3600, TokenType: "Bearer"})
			}))
			defer server.Close()
			manager := &kimiCodeManager{
				host: Host{Home: root, Env: func(key string) string {
					if key == "KIMI_CODE_HOME" {
						return root
					}
					return ""
				}},
				client: server.Client(), tokenURL: server.URL, now: func() time.Time { return now },
			}
			rows, err := manager.Capture(context.Background(), "")
			if err != nil || len(rows) != 1 {
				t.Fatalf("Capture = %+v, %v", rows, err)
			}
			current := rows[0]
			if !manager.Caps().Refresh {
				t.Fatal("native Kimi renewal is disabled")
			}
			calls := 0
			sentinel := errors.New("checkpoint failed")
			got, err := manager.RefreshActive(context.Background(), current, func(next Captured) error {
				calls++
				native, captureErr := manager.Capture(context.Background(), "")
				if captureErr != nil || len(native) != 1 || NewerCredential(native[0], current) || !NewerCredential(next, current) {
					t.Error("checkpoint happened after native write or lost the rotation")
				}
				if failCheckpoint {
					return sentinel
				}
				return nil
			})
			if calls != 1 || !NewerCredential(got, current) {
				t.Fatalf("renewal was lost: %+v, %v", got, err)
			}
			native, captureErr := manager.Capture(context.Background(), "")
			if failCheckpoint {
				if !errors.Is(err, sentinel) || captureErr != nil || NewerCredential(native[0], current) {
					t.Fatalf("checkpoint failure wrote native state: %v, %+v", err, native)
				}
			} else if err != nil || captureErr != nil || !NewerCredential(native[0], current) {
				t.Fatalf("active renewal was not restored: %v, %+v", err, native)
			}
		})
	}
}
