package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestGrokRefreshRotatesAndPreservesNativeFields(t *testing.T) {
	c := grokUsageFixture(t)
	c.Credential.Payload = []byte(strings.Replace(string(c.Credential.Payload), `"key":"fixture-session"`, `"key":"fixture-session","refresh_token":"old-refresh","expires_at":"2026-01-01T00:00:00Z","future_field":true`, 1))
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://auth.x.ai/oauth2/token" || r.Method != "POST" {
			t.Fatal("wrong refresh endpoint")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("client_id") != grokOIDCClient {
			t.Fatal("wrong refresh request")
		}
		return directTestResponse(200, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`), nil
	})
	next, err := refreshGrokSaved(context.Background(), c, client, now)
	if err != nil {
		t.Fatal(err)
	}
	var files map[string]json.RawMessage
	var auth map[string]map[string]json.RawMessage
	_ = json.Unmarshal(next.Credential.Payload, &files)
	_ = json.Unmarshal(files["auth.json"], &auth)
	session := auth[grokOIDCIssuer+"::"+grokOIDCClient]
	if jsonString(session["key"]) != "new-access" || jsonString(session["refresh_token"]) != "new-refresh" || string(session["future_field"]) != "true" || jsonString(session["expires_at"]) != now.Add(time.Hour).Format(time.RFC3339) || next.Identity != c.Identity {
		t.Fatal("rotation lost native fields or identity")
	}
	if strings.Contains(string(c.Credential.Payload), "new-access") {
		t.Fatal("mutated input credential")
	}
}

func TestGrokRefreshFailureDoesNotMutateOrInventSignIn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		network bool
		invalid bool
	}{
		{"revoked", 400, `{"error":"invalid_grant"}`, false, true},
		{"server", 503, `private-provider-body`, false, false},
		{"network", 0, "", true, false},
		{"malformed", 200, `{}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := grokUsageFixture(t)
			c.Credential.Payload = []byte(strings.Replace(string(c.Credential.Payload), `"key":"fixture-session"`, `"key":"fixture-session","refresh_token":"old-refresh"`, 1))
			before := string(c.Credential.Payload)
			client := directTestClient(func(*http.Request) (*http.Response, error) {
				if tc.network {
					return nil, errors.New("private-network-secret")
				}
				return directTestResponse(tc.status, tc.body), nil
			})
			_, err := refreshGrokSaved(context.Background(), c, client, time.Now())
			if err == nil || errors.Is(err, ErrInvalidGrant) != tc.invalid || strings.Contains(err.Error(), "private-") || string(c.Credential.Payload) != before {
				t.Fatal("unsafe refresh error", err)
			}
		})
	}
}

func TestGrokUsageRejectionDoesNotInvalidateRefreshGrant(t *testing.T) {
	client := directTestClient(func(*http.Request) (*http.Response, error) {
		return directTestResponse(401, `private-provider-body`), nil
	})
	_, err := probeGrokSaved(context.Background(), grokUsageFixture(t), client, time.Now())
	var status *UsageHTTPError
	if !errors.As(err, &status) || status.Status != 401 || errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("billing rejection incorrectly invalidated refresh grant: %v", err)
	}
}
