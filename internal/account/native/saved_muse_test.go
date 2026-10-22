package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMuseSavedUsageReadsNativeMintResponse(t *testing.T) {
	c := Captured{Identity: "meta:email:fixture@example.test", Credential: museTestCredential(t, `{"mechanism":"oauth","access_token":"fixture-access","api_key":"fixture-old-key","user_email":"fixture@example.test"}`)}
	before := append([]byte(nil), c.Credential.Payload...)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	http.DefaultTransport = directTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.String() != "https://api.meta.ai/muse-code/key" || r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("x-api-version") != "1.0.0" || r.Header.Get("Content-Type") != "application/json" || string(body) != `{"onboard":false}` {
			t.Fatal("wrong Muse mint request")
		}
		return directTestResponse(200, `{"api_key":"private-minted-key","user_email":"fixture@example.test","subs_tier_id":"max","subs_tier_name":"Muse Code Max Usage","is_subs_active":true,"subs_usage":{"window":{"used_percent":12.5,"window_duration_mins":300,"resets_at":2000000000},"weekly":{"used_percent":45,"resets_at":2000600000}}}`), nil
	})
	o, err := (&museManager{}).ProbeSaved(context.Background(), c)
	if err != nil || calls != 1 || len(o.Windows) != 2 {
		t.Fatalf("usage=%+v err=%v calls=%d", o, err, calls)
	}
	if *o.Windows[0].UsedPercent != 12.5 || o.Windows[0].ResetsAt != "2033-05-18T03:33:20Z" || *o.Windows[1].UsedPercent != 45 || o.Windows[1].Period != "week" || o.Plan == nil || o.Plan.Label != "Muse Code Max Usage" || o.ObservedAt.IsZero() {
		t.Fatal("incorrect Muse quota projection", o)
	}
	if !reflect.DeepEqual(before, []byte(c.Credential.Payload)) {
		t.Fatal("changed saved login")
	}
}

func TestMuseSavedUsageHandlesAbsentPercentAndRejectsInvalidData(t *testing.T) {
	c := Captured{Identity: "meta:email:fixture@example.test", Credential: museTestCredential(t, `{"mechanism":"oauth","access_token":"fixture-access","user_email":"fixture@example.test"}`)}
	for _, tc := range []struct {
		name, body string
		wantError  bool
		reset      string
	}{
		{"plan only", `{"user_email":"fixture@example.test","subs_tier_id":"max","subs_tier_name":"Muse Code Max Usage","is_subs_active":true}`, false, ""},
		{"plan only null", `{"user_email":"fixture@example.test","subs_tier_id":"max","subs_tier_name":"Muse Code Max Usage","is_subs_active":true,"subs_usage":null}`, false, ""},
		{"inactive", `{"user_email":"fixture@example.test","is_subs_active":false}`, true, ""},
		{"reset only", `{"user_email":"fixture@example.test","subs_usage":{"weekly":{"resets_at":2000000000}}}`, false, "2033-05-18T03:33:20Z"},
		{"wrong identity", `{"user_email":"other@example.test"}`, true, ""},
		{"missing identity", `{}`, true, ""},
		{"negative percent", `{"user_email":"fixture@example.test","subs_usage":{"weekly":{"used_percent":-1,"resets_at":2000000000}}}`, true, ""},
		{"invalid reset", `{"user_email":"fixture@example.test","subs_usage":{"weekly":{"used_percent":1,"resets_at":"invalid"}}}`, true, ""},
		{"invalid duration", `{"user_email":"fixture@example.test","subs_usage":{"window":{"used_percent":1,"resets_at":2000000000,"window_duration_mins":0}}}`, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = directTestTransport(func(*http.Request) (*http.Response, error) { return directTestResponse(200, tc.body), nil })
			o, err := (&museManager{}).ProbeSaved(context.Background(), c)
			if strings.HasPrefix(tc.name, "plan only") {
				if !errors.Is(err, ErrUsageIdle) || !o.ObservedAt.IsZero() || len(o.Windows) != 0 || o.Plan == nil || o.Plan.Label != "Muse Code Max Usage" {
					t.Fatal("idle response misrepresented", o, err)
				}
				return
			}
			if (err != nil) != tc.wantError {
				t.Fatalf("usage=%+v err=%v", o, err)
			}
			if tc.wantError {
				return
			}
			if len(o.Windows) != 1 || o.Windows[0].UsedPercent != nil || o.Windows[0].ResetsAt != tc.reset || o.Windows[0].UnavailableReason == "" || o.UsageError != "" || o.Unavailable {
				t.Fatal("missing quota data was misrepresented", o)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&museManager{}).ProbeSaved(ctx, c); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestMuseUsageAcceptsUnexpiredTokenAndRequiresLoginWithoutFallback(t *testing.T) {
	for _, tc := range []struct {
		name        string
		expiry      time.Time
		status      int
		wantInvalid bool
	}{
		{"expires soon", time.Now().Add(20 * time.Second), 200, false},
		{"expired", time.Now().Add(-time.Hour), 200, true},
		{"rejected", time.Now().Add(time.Hour), 401, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"access_token":"fixture-access","user_email":"fixture@example.test","expires_at":%d}`, tc.expiry.Unix())
			captured := Captured{Identity: "meta:email:fixture@example.test", Credential: museTestCredential(t, raw)}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			calls := 0
			http.DefaultTransport = directTestTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return directTestResponse(tc.status, `{"user_email":"fixture@example.test","subs_usage":{"weekly":{"used_percent":12,"resets_at":2000000000}}}`), nil
			})
			_, err := (&museManager{}).ProbeSaved(t.Context(), captured)
			if errors.Is(err, ErrInvalidGrant) != tc.wantInvalid || (!tc.wantInvalid && err != nil) {
				t.Fatalf("usage error=%v", err)
			}
			expected := 1
			if tc.name == "expired" {
				expected = 0
			}
			if calls != expected {
				t.Fatalf("requests=%d want %d", calls, expected)
			}
		})
	}
}
