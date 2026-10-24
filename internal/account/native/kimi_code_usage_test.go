package native

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestKimiCodeUsageReadsNativeCredentialAndSkipsExpiredToken(t *testing.T) {
	now := time.Unix(1900000000, 0).UTC()
	newManager := func(expiresAt time.Time) (*kimiCodeManager, Captured) {
		root := t.TempDir()
		config := []byte(`default_model = "kimi-code/k3"
[providers."managed:kimi-code"]
type = "kimi"
base_url = "https://api.kimi.com/coding/v1"
[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code"
`)
		token := []byte(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_at":` + jsonInt(expiresAt.Unix()) + `,"token_type":"Bearer","expires_in":3600}`)
		if err := os.MkdirAll(filepath.Join(root, "credentials"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "config.toml"), config, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "credentials", "kimi-code.json"), token, 0600); err != nil {
			t.Fatal(err)
		}
		manager := &kimiCodeManager{host: Host{Home: root, Env: func(key string) string {
			if key == "KIMI_CODE_HOME" {
				return root
			}
			return ""
		}}, now: func() time.Time { return now }}
		rows, err := manager.Capture(context.Background(), "")
		if err != nil || len(rows) != 1 {
			t.Fatalf("Capture = %+v, %v", rows, err)
		}
		return manager, rows[0]
	}

	manager, captured := newManager(now.Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usages" || r.Header.Get("Authorization") != "Bearer fixture-access" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected usage request: path=%q accept=%q authorized=%v", r.URL.Path, r.Header.Get("Accept"), r.Header.Get("Authorization") != "")
		}
		_, _ = w.Write([]byte(`{"usages":{"limit_5h":{"used_ratio":0.1241,"reset_time":"2026-10-24T15:22:12Z"},"limit_7d":{"used_ratio":0.2,"reset_time":"2026-10-31T00:00:00Z"},"limit_month_total":{"used_ratio":0.4,"reset_time":"2026-11-24T00:00:00Z"},"limit_month_code":{"used_ratio":0.25,"reset_time":"2026-11-24T00:00:00Z"}}}`))
	}))
	defer server.Close()
	manager.client, manager.usageURL = server.Client(), server.URL+"/usages"
	observation, err := manager.ProbeSaved(context.Background(), captured)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.ObservedAt.Equal(now) || len(observation.Windows) != 4 {
		t.Fatalf("observation = %+v", observation)
	}
	want := []struct {
		id, label string
		percent   float64
		reset     string
	}{
		{"five_hour", "5-hour", 12.41, "2026-10-24T15:22:12Z"},
		{"seven_day", "Weekly", 20, "2026-10-31T00:00:00Z"},
		{"monthly", "Monthly", 40, "2026-11-24T00:00:00Z"},
		{"monthly_code", "Monthly Code", 25, "2026-11-24T00:00:00Z"},
	}
	for i, expected := range want {
		window := observation.Windows[i]
		if window.ID != expected.id || window.Label != expected.label || window.UsedPercent == nil || math.Abs(*window.UsedPercent-expected.percent) > 1e-9 || window.ResetsAt != expected.reset {
			t.Errorf("window %d = %+v, want %+v", i, window, expected)
		}
	}

	expired, expiredLogin := newManager(now.Add(-time.Hour))
	requests := 0
	expiredServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer expiredServer.Close()
	expired.client, expired.usageURL = expiredServer.Client(), expiredServer.URL+"/usages"
	_, err = expired.ProbeSaved(context.Background(), expiredLogin)
	var status *UsageHTTPError
	if !errors.As(err, &status) || status.Status != http.StatusUnauthorized || errors.Is(err, ErrInvalidGrant) || requests != 0 {
		t.Fatalf("expired token probe = %v, requests = %d", err, requests)
	}
}

func TestKimiCodeUsageRejectsConflictingCountAndRatio(t *testing.T) {
	payload := []byte(`{
  "usage": {"limit": "100", "used": "100", "resetTime": "2026-09-24T02:09:07.465054Z"},
  "limits": [{"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}, "detail": {"limit": "100", "remaining": "100", "resetTime": "2026-09-21T01:09:07.465054Z"}}],
  "usages": {
    "limit_5h": {"used_ratio": 0, "reset_time": "2026-09-21T01:09:06Z"},
    "limit_7d": {"used_ratio": 0, "reset_time": "2026-09-24T02:09:06Z"}
  }
}`)
	if _, err := kimiCodeUsageObservation(payload, time.Now()); !errors.Is(err, errKimiCodeUsageConflict) {
		t.Fatalf("conflicting count and ratio = %v, want conflict", err)
	}
	consistent := []byte(strings.Replace(string(payload), `"used": "100"`, `"used": "0"`, 1))
	observation, err := kimiCodeUsageObservation(consistent, time.Now())
	if err != nil || len(observation.Windows) != 2 {
		t.Fatalf("consistent count and ratio = %+v, %v", observation, err)
	}
}

func TestKimiCodeUsageAllowsRoundedCountRatios(t *testing.T) {
	payload := []byte(`{
  "usage": {"limit": "100", "used": "1", "resetTime": "2026-09-24T02:09:07.465054Z"},
  "limits": [{"window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"}, "detail": {"limit": "100", "remaining": "96", "resetTime": "2026-09-21T01:09:07.465054Z"}}],
  "usages": {
    "limit_5h": {"used_ratio": 0.038, "reset_time": "2026-09-21T01:09:06Z"},
    "limit_7d": {"used_ratio": 0.0077, "reset_time": "2026-09-24T02:09:06Z"}
  }
}`)
	observation, err := kimiCodeUsageObservation(payload, time.Now())
	if err != nil || len(observation.Windows) != 2 {
		t.Fatalf("rounded count and ratio = %+v, %v", observation, err)
	}
	if math.Abs(*observation.Windows[0].UsedPercent-3.8) > 1e-9 || math.Abs(*observation.Windows[1].UsedPercent-0.77) > 1e-9 {
		t.Fatalf("precise ratios were not preserved: %+v", observation.Windows)
	}
}

func jsonInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
