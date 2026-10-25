package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

func grokUsageFixture(t *testing.T) Captured {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"auth.json": map[string]any{grokOIDCIssuer + "::" + grokOIDCClient: map[string]string{
		"key": "fixture-session", "auth_mode": "oidc", "user_id": "fixture-user", "oidc_issuer": grokOIDCIssuer, "oidc_client_id": grokOIDCClient,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return Captured{Identity: "grok:native", Credential: store.Credential{Schema: "grok/native-files@1", Payload: payload}}
}

const grokWeeklyFixture = `{"config":{"isUnifiedBillingUser":true,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-18T05:11:47.565+00:00","end":"2026-10-25T05:11:47.565+00:00"},"prepaidBalance":{"val":0}}}`

func TestSavedGrokBillingUsesNativeSessionAndReadsTheZeroWindow(t *testing.T) {
	c := grokUsageFixture(t)
	before := append([]byte(nil), c.Credential.Payload...)
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	calls := 0
	client := directTestClient(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.String() != "https://cli-chat-proxy.grok.com/v1/billing?format=credits" || r.Header.Get("Authorization") != "Bearer fixture-session" || r.Header.Get("x-userid") != "fixture-user" || r.Header.Get("x-xai-token-auth") != "xai-grok-cli" || r.Header.Get("x-grok-client-version") != "1.0.62" {
			t.Fatal("incorrect verified billing request")
		}
		return directTestResponse(200, grokWeeklyFixture), nil
	})
	observation, err := probeGrokSaved(context.Background(), c, client, now)
	if err != nil || calls != 1 || len(observation.Windows) != 1 || !observation.ObservedAt.Equal(now) {
		t.Fatal("missing weekly observation", err)
	}
	w := observation.Windows[0]
	if w.UsedPercent == nil || *w.UsedPercent != 0 || w.UnavailableReason != "" {
		t.Fatalf("weekly period did not read the omitted zero: %+v", w)
	}
	if w.Label != "Weekly" || w.Period != "week" || w.ResetsAt != "2026-10-25T05:11:47.565Z" || w.PeriodStart != "2026-10-18T05:11:47.565Z" || w.Used != nil || w.Limit != nil {
		t.Fatal("wrong weekly window", w)
	}
	// isUnifiedBillingUser is the X Subscription: Grok's shared weekly pool.
	if observation.Plan == nil || observation.Plan.ID != "x-subscription" || observation.Plan.Label != "X Subscription (weekly)" {
		t.Fatal("unified billing user is not on the X Subscription", observation.Plan)
	}
	if !reflect.DeepEqual(before, []byte(c.Credential.Payload)) {
		t.Fatal("saved native login changed")
	}
	// The CLI displays 1.0 as 1% used; the API value is already percentage points.
	raw := strings.Replace(grokWeeklyFixture, `"prepaidBalance"`, `"creditUsagePercent":0.5,"prepaidBalance"`, 1)
	observation, err = grokBillingObservation([]byte(raw), now)
	if err != nil || observation.Windows[0].UsedPercent == nil || *observation.Windows[0].UsedPercent != 0.5 {
		t.Fatal("reported percentage was lost or rescaled", err)
	}
}
func TestSavedGrokBillingRejectsWrongAuthWithoutInvalidatingGrant(t *testing.T) {
	for _, status := range []int{401, 403} {
		client := directTestClient(func(*http.Request) (*http.Response, error) {
			return directTestResponse(status, "private-provider-error"), nil
		})
		_, err := probeGrokSaved(context.Background(), grokUsageFixture(t), client, time.Now())
		if err == nil || errors.Is(err, ErrInvalidGrant) || strings.Contains(err.Error(), "private-provider-error") {
			t.Fatal("billing rejection invalidated the refresh grant or exposed provider data", err)
		}
	}
	for _, replace := range [][2]string{{"fixture-user", ""}, {"fixture-session", "bad\\r\\nheader"}, {"oidc_issuer", "other_issuer"}, {"oidc", "api-key"}} {
		c := grokUsageFixture(t)
		c.Credential.Payload = []byte(strings.ReplaceAll(string(c.Credential.Payload), replace[0], replace[1]))
		client := directTestClient(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid login reached provider")
			return nil, nil
		})
		if _, err := probeGrokSaved(context.Background(), c, client, time.Now()); err == nil {
			t.Fatal("accepted invalid login")
		}
	}
}
func TestGrokBillingRejectsUnusablePeriods(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"config":{}}`,
		strings.Replace(grokWeeklyFixture, "2026-10-18", "2026-10-26", 1),
		strings.Replace(grokWeeklyFixture, "2026-10-25", "invalid", 1),
		// An explicit percentage must not excuse an unusable weekly or monthly period.
		`{"config":{"isUnifiedBillingUser":true,"creditUsagePercent":42,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-18T05:11:47.565+00:00","end":"invalid"}}}`,
		`{"config":{"isUnifiedBillingUser":true,"creditUsagePercent":42,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-26T05:11:47.565+00:00","end":"2026-10-25T05:11:47.565+00:00"}}}`,
		`{"config":{"isUnifiedBillingUser":true,"creditUsagePercent":42,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY"}}}`,
		`{"config":{"isUnifiedBillingUser":false,"creditUsagePercent":42,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY"}}}`,
	} {
		if _, err := grokBillingObservation([]byte(raw), time.Now()); err == nil {
			t.Fatal("accepted an unusable period")
		}
	}
}

func TestGrokBillingReadsMonthlyWindow(t *testing.T) {
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	raw := `{"config":{"isUnifiedBillingUser":false,"creditUsagePercent":42,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","start":"2026-10-01T00:00:00Z","end":"2026-11-01T00:00:00Z"}}}`
	observation, err := grokBillingObservation([]byte(raw), now)
	if err != nil || len(observation.Windows) != 1 {
		t.Fatalf("monthly window missing: %+v, %v", observation, err)
	}
	w := observation.Windows[0]
	if w.ID != "grok-monthly" || w.Label != "Monthly" || w.Period != "month" || w.UsedPercent == nil || *w.UsedPercent != 42 || w.ResetsAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("wrong monthly window: %+v", w)
	}
	if observation.Plan == nil || observation.Plan.ID != "grok-legacy" {
		t.Fatalf("legacy monthly plan not named: %+v", observation.Plan)
	}
}

// A legacy billing user with no period data is named; no window is invented.
func TestGrokLegacyBillingNamesThePlanWithoutUsage(t *testing.T) {
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	observation, err := grokBillingObservation([]byte(`{"config":{"isUnifiedBillingUser":false,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY"}}}`), now)
	if err != nil || observation.Plan == nil || observation.Plan.ID != "grok-legacy" || observation.Plan.Label != "Legacy (monthly)" || len(observation.Windows) != 0 || !observation.ObservedAt.Equal(now) {
		t.Fatal("legacy plan not named", observation, err)
	}
}

func TestGrokBillingReadsRemainingAllowance(t *testing.T) {
	for _, percent := range []string{"0", "0.5", "1", "25", "100"} {
		t.Run(percent, func(t *testing.T) {
			raw := strings.Replace(grokWeeklyFixture, `"prepaidBalance"`, `"creditUsagePercent":`+percent+`,"prepaidBalance"`, 1)
			observation, err := grokBillingObservation([]byte(raw), time.Now())
			if err != nil || len(observation.Windows) != 1 {
				t.Fatal(err)
			}
			w := observation.Windows[0]
			var want float64
			if err := json.Unmarshal([]byte(percent), &want); err != nil {
				t.Fatal(err)
			}
			if w.UsedPercent == nil || *w.UsedPercent != want || w.UnavailableReason != "" || w.Unit != "percent" {
				t.Fatal("reported allowance is unavailable", w)
			}
		})
	}
}

func TestGrokBillingRejectsInvalidPercentages(t *testing.T) {
	for _, percent := range []string{"-1", "101", "1e999", `"25"`} {
		raw := strings.Replace(grokWeeklyFixture, `"prepaidBalance"`, `"creditUsagePercent":`+percent+`,"prepaidBalance"`, 1)
		if _, err := grokBillingObservation([]byte(raw), time.Now()); err == nil {
			t.Fatal("invalid percentage accepted", percent)
		}
	}
}
