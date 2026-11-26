package pig

import "testing"

func TestModelRetryResetFromPiGError(t *testing.T) {
	for _, tc := range []struct {
		message string
		seconds int64
	}{
		{`429: {"code":"model_cooldown","reset_seconds":120}`, 120},
		{`429 {"error":{"code":"model_cooldown","reset_seconds":124}}`, 124},
		{`429 {"code":"1302","message":"Rate limit reached for requests","reset_seconds":8}`, 8},
		{`429 {"error":{"code":1305,"message":"The service may be temporarily overloaded","reset_seconds":62}}`, 62},
		{`503: {"code":"server_is_overloaded","reset_seconds":60}`, 60},
		{`503: {"error":"Overloaded","reset_seconds":60}`, 60},
		{`429 {"type":"error","error":{"type":"rate_limit_error","message":"All credentials for model claude are cooling down","reset_seconds":120}}`, 120},
		{`Server requested 120s retry delay (max: 20s). 429 {"type":"error","error":{"type":"rate_limit_error","message":"All credentials for model claude are cooling down"}}`, 120},
		{`OpenAI API error (429): {"code":"model_cooldown","reset_seconds":120}`, 120},
		{`Server requested 65s retry delay (max: 20s). 502 status code (no body)`, 65},
		{`Server requested 70s retry delay (max: 20s). 503 Service Unavailable. {"error":{"type":"server_error"}}`, 70},
		{`Server requested 80s retry delay (max: 20s). 429 The service may be temporarily overloaded`, 80},
		{`429 {"code":"1302","message":"Rate limit reached for requests"}`, 60},
		{`429 {"error":{"code":1305,"message":"The service may be temporarily overloaded"}}`, 20},
		{`400 {"code":"1302","message":"Rate limit reached for requests"}`, 0},
		{`429: {"code":"insufficient_quota","reset_seconds":100}`, 100},
		{`OpenAI API error (429): {"code":"usage_limit_reached","reset_seconds":259200}`, 259200},
		{`Server requested 259200s retry delay (max: 1s). 429 Weekly quota exhausted`, 259200},
		{`Server requested 7200s retry delay (max: 1s). 429 Rate limit reached for requests`, 0},
		{`429 {"error":{"code":"dyna_quota_spent","reset_seconds":2678400}}`, 2678400},
		{`Server requested 259200s retry delay (max: 20s). 429 {"error":{"type":"rate_limit_error","reset_seconds":259200}}`, 259200},
		{`429 {"error":{"code":"dyna_quota_spent","reset_seconds":2678401}}`, 0},
		{`429 {"error":{"code":"transient_rate_limit","reset_seconds":7200}}`, 0},
		{`503: {"reset_seconds":-1}`, 0},
		{`429: {"code":"model_cooldown","reset_seconds":86400}`, 86400},
		{`Server requested 3601s retry delay (max: 20s). 502 status code (no body)`, 0},
		{`503: {"reset_seconds":9223372036854775807}`, 0},
		{`unrelated error: 503: {"reset_seconds":500}`, 0},
		{`502 status code (no body)`, 0},
	} {
		if got := modelRetryAfterSeconds(tc.message); got != tc.seconds {
			t.Errorf("%q: got %d, want %d", tc.message, got, tc.seconds)
		}
	}
}
