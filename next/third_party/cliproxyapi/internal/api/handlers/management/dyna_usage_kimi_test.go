package management

import (
	"net/http"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestDynaUsageKimiOfficialShape(t *testing.T) {
	usage := dynaParse(t, parseKimiUsage, `{"usages":{
		"limit_5h":{"used_ratio":0.0878,"reset_time":"2026-10-06T14:51:00Z"},
		"limit_7d":{"used_ratio":"0.2588","reset_time":"2026-10-12T12:51:00Z"},
		"limit_month_total":{"used_ratio":0.5531,"reset_time":"2026-10-22T14:26:29Z"},
		"limit_month_code":{"used_ratio":0.1,"reset_time":"2026-10-22T14:26:29Z"}}}`)
	if session := dynaWindowNamed(t, usage, "five_hour"); session.Kind != "five_hour" || session.Utilization != 8.78 {
		t.Fatalf("session = %#v", session)
	}
	if weekly := dynaWindowNamed(t, usage, "seven_day"); weekly.Utilization != 25.88 {
		t.Fatalf("weekly = %#v", weekly)
	}
	if monthly := dynaWindowNamed(t, usage, "monthly"); monthly.Kind != "monthly" || monthly.Utilization != 55.31 || monthly.ResetsAt == "" {
		t.Fatalf("monthly = %#v", monthly)
	}
	if code := dynaWindowNamed(t, usage, "Code monthly"); code.Scope != "Code" {
		t.Fatalf("code = %#v", code)
	}
}

// The self-contradictory response from CodexBar issue #4306: an exhausted legacy
// counter beside a zero ratio with the same reset, and a monthly pool.
func TestDynaUsageKimiMoreExhaustedReadingWins(t *testing.T) {
	usage := dynaParse(t, parseKimiUsage, `{
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},
			"detail":{"limit":"100","used":"100","resetTime":"2026-10-06T13:23:46.915474Z"}}],
		"usages":{
			"limit_5h":{"used_ratio":0,"reset_time":"2026-10-06T13:23:46Z"},
			"limit_month_total":{"used_ratio":0.5531,"reset_time":"2026-10-22T14:26:29Z"},
			"limit_month_code":{"used_ratio":0,"reset_time":"2026-10-22T14:26:29Z"}}}`)
	if session := dynaWindowNamed(t, usage, "five_hour"); session.Utilization != 100 {
		t.Fatalf("session = %#v, want the exhausted reading", session)
	}
	if monthly := dynaWindowNamed(t, usage, "monthly"); monthly.Utilization != 55.31 {
		t.Fatalf("monthly = %#v, want the total pool shown", monthly)
	}
}

// The shape from the comments on kimi-code #4133 and #3817: zero ratios beside
// counters saying 10 of 100, with no monthly pools.
func TestDynaUsageKimiFalseZeroIsCorrected(t *testing.T) {
	usage := dynaParse(t, parseKimiUsage, `{
		"usage":{"limit":"100","used":"10","resetTime":"2026-10-12T12:51:00Z"},
		"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},
			"detail":{"limit":"100","used":"10","resetTime":"2026-10-06T14:51:00Z"}}],
		"usages":{
			"limit_5h":{"used_ratio":0,"reset_time":"2026-10-06T14:51:00Z"},
			"limit_7d":{"used_ratio":0,"reset_time":"2026-10-12T12:51:00Z"}}}`)
	if dynaWindowNamed(t, usage, "five_hour").Utilization != 10 || dynaWindowNamed(t, usage, "seven_day").Utilization != 10 {
		t.Fatalf("windows = %#v", usage.windows)
	}
	if len(usage.windows) != 2 {
		t.Fatalf("windows = %#v, want no monthly window", usage.windows)
	}
}

func TestDynaUsageKimiRatioWinsWhenResetsDiffer(t *testing.T) {
	usage := dynaParse(t, parseKimiUsage, `{
		"limits":[{"window":{"duration":5,"timeUnit":"TIME_UNIT_HOUR"},"detail":{"limit":"100","used":"90","resetTime":"2026-10-06T10:00:00Z"}}],
		"usages":{"limit_5h":{"used_ratio":0.2,"reset_time":"2026-10-06T14:51:00Z"}}}`)
	if session := dynaWindowNamed(t, usage, "five_hour"); session.Utilization != 20 {
		t.Fatalf("session = %#v, want the ratio: the counter is from an earlier window", session)
	}
}

func TestDynaUsageKimiLegacyOnly(t *testing.T) {
	usage := dynaParse(t, parseKimiUsage, `{"usage":{"limit":100,"remaining":75,"resetTime":"2026-10-12T12:51:00Z"},
		"limits":[{"window":{"duration":1,"timeUnit":"TIME_UNIT_DAY"},"detail":{"limit":"200","used":"50"}}]}`)
	if dynaWindowNamed(t, usage, "seven_day").Utilization != 25 || dynaWindowNamed(t, usage, "window_86400").Utilization != 25 {
		t.Fatalf("windows = %#v", usage.windows)
	}
}

func TestDynaUsageKimiBooster(t *testing.T) {
	usage := dynaParse(t, parseKimiUsage, `{"boosterWallet":{"balance":{"type":"BOOSTER","amount":"2000000000","amountLeft":"1234000000"},"monthlyChargeLimit":{"priceInCents":"500","currency":"EUR"}}}`)
	if len(usage.balances) != 1 || usage.balances[0] != (dynaQuotaBalance{Currency: "EUR", Amount: "12.34"}) {
		t.Fatalf("balances = %#v", usage.balances)
	}
	for _, body := range []string{`{"boosterWallet":null}`, `{"boosterWallet":{"balance":{"type":"OTHER","amount":"1"}}}`, `{"boosterWallet":{"balance":{"type":"BOOSTER","amount":"0"}}}`} {
		if usage := dynaParse(t, parseKimiUsage, body); len(usage.balances) != 0 {
			t.Fatalf("%s produced %#v", body, usage.balances)
		}
	}
}

func TestDynaUsageKimiUnknownShapeIsNotZero(t *testing.T) {
	for _, body := range []string{`{}`, `{"usages":{}}`, `{"usages":{"limit_5h":{"reset_time":"2026-10-06T14:51:00Z"}}}`, `{"limits":[{"detail":{"limit":"0","used":"0"},"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"}}]}`} {
		if usage := dynaParse(t, parseKimiUsage, body); len(usage.windows) != 0 {
			t.Fatalf("%s produced %#v", body, usage.windows)
		}
	}
}

func TestDynaUsageKimiHostFollowsTheCredentialDomain(t *testing.T) {
	for _, tc := range []struct{ domain, url string }{
		{"", "https://api.kimi.com/coding/v1/usages"},
		{"kimi.ai", "https://api.kimi.ai/coding/v1/usages"},
	} {
		fake := dynaFixedUsage(t, http.StatusUnauthorized, ``)
		metadata := map[string]any{"access_token": "invented"}
		if tc.domain != "" {
			metadata["domain"] = tc.domain
		}
		entry := dynaOneEntry(t, dynaUsageHandler(t, &coreauth.Auth{ID: "invented", Provider: "kimi", Metadata: metadata}))
		// A 401 here says nothing about the sign-in.
		if entry.State != dynaUsageUnreachable {
			t.Fatalf("entry = %#v", entry)
		}
		if got := fake.requests[0].URL.String(); got != tc.url {
			t.Fatalf("url = %q, want %q", got, tc.url)
		}
	}
}
