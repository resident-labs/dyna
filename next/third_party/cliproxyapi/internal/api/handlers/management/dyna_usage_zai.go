package management

import (
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// requestZAIUsage sends the key raw, as z.ai's plugin and cc-switch do.
func requestZAIUsage(auth *coreauth.Auth) (string, http.Header) {
	key := dynaAPIKey(auth)
	if key == "" {
		return "", nil
	}
	header := make(http.Header)
	header.Set("Authorization", key)
	header.Set("Accept", "application/json")
	return "https://api.z.ai/api/monitor/usage/quota/limit", header
}

// parseZAIUsage follows farion1231/cc-switch services/coding_plan.rs. A
// pay-as-you-go key answers HTTP 200 with success false. A limit's unit names its
// window, because a week near its end resets before five hours do. TIME_LIMIT
// counts tool calls, not coding use.
func parseZAIUsage(root gjson.Result, now time.Time) dynaUsage {
	var usage dynaUsage
	if success := root.Get("success"); success.Exists() && !success.Bool() {
		return usage
	}
	if code := root.Get("code"); code.Exists() && code.Int() != http.StatusOK {
		return usage
	}
	for _, limit := range root.Get("data.limits").Array() {
		switch strings.ToUpper(limit.Get("type").String()) {
		case "TOKENS_LIMIT", "CREDIT_LIMIT":
		default:
			continue
		}
		var seconds int64
		switch limit.Get("unit").Int() {
		case 3:
			seconds = dynaFiveHourSeconds
		case 6:
			seconds = dynaWeekSeconds
		default:
			continue
		}
		if used, ok := dynaPercent(limit.Get("percentage")); ok {
			usage.addWindow(seconds, "", used, dynaTime(limit.Get("nextResetTime")), now)
		}
	}
	return usage
}
