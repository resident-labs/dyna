package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type codexUsageTransport func(*http.Request) (*http.Response, error)

func (f codexUsageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexSavedUsage(t *testing.T) {
	const response = `{"plan_type":"promax","rate_limit":{"primary_window":{"used_percent":39,"limit_window_seconds":604800,"reset_at":1792875197},"secondary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_at":1792275197}}}`
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr error
		windows int
	}{
		{"weekly_and_session", 200, response, nil, 2},
		{"default_client", 200, response, nil, 2},
		{"expired", 401, "private response", ErrInvalidGrant, 0},
		{"forbidden", 403, "private response", ErrInvalidGrant, 0},
		{"limited", 429, "private response", nil, 0},
		{"schema", 200, `{"rate_limit":{}}`, nil, 0},
		{"malformed", 200, `not json`, nil, 0},
		{"missing_percent", 200, `{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"reset_at":1792875197}}}`, nil, 0},
		{"invalid_percent", 200, strings.Replace(response, `"used_percent":39`, `"used_percent":139`, 1), nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := codexTestManager(t)
			captured, err := codexParse(codexTestAuth("user", "workspace", "saved-refresh", time.Now().Add(time.Hour)))
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(captured.Credential)
			calls := 0
			transport := codexUsageTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") || req.Header.Get("ChatGPT-Account-Id") != "workspace" {
					t.Fatal("wrong usage request")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{"17"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			if tc.name == "default_client" {
				previous := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = previous })
				http.DefaultTransport = transport
			} else {
				m.client = &http.Client{Transport: transport}
			}
			prober, ok := any(m).(SavedProber)
			if !ok {
				t.Fatal("Codex manager does not implement SavedProber")
			}
			observation, err := prober.ProbeSaved(context.Background(), captured)
			if calls != 1 || len(observation.Windows) != tc.windows {
				t.Fatalf("calls=%d observation=%+v err=%v", calls, observation, err)
			}
			if tc.windows > 0 {
				if err != nil || observation.ObservedAt.IsZero() || observation.Plan.ID != "promax" || observation.Windows[0].ID != "seven_day" || *observation.Windows[0].UsedPercent != 39 || *observation.Windows[1].UsedPercent != 0 {
					t.Fatalf("bad quota: %+v %v", observation, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private response") {
				t.Fatalf("missing or unsafe error: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
			if tc.status == 429 {
				var quota *QuotaError
				if !errors.As(err, &quota) || quota.RetryAfter != 17*time.Second {
					t.Fatalf("quota error=%v", err)
				}
			}
			after, _ := json.Marshal(captured.Credential)
			if string(before) != string(after) {
				t.Fatal("probe changed credential")
			}
		})
	}
}
