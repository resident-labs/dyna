package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// requestXAIUsage follows xai-org/grok-build extensions/billing.rs, with the
// client version the proxy already keeps current.
func requestXAIUsage(auth *coreauth.Auth) (string, http.Header) {
	token := dynaAccessToken(auth)
	if token == "" {
		return "", nil
	}
	header := dynaBearer(token)
	header.Set("x-xai-token-auth", "xai-grok-cli")
	header.Set("x-grok-client-version", helps.GetXAIClientVersion())
	if subject := stringValue(auth.Metadata, "sub"); subject != "" {
		header.Set("x-userid", subject)
	}
	return "https://cli-chat-proxy.grok.com/v1/billing?format=credits", header
}

// parseXAIUsage prefers creditUsagePercent, then the deprecated monthlyLimit and
// used. proto3 omits a zero percent, so a missing one means 0 only beside a
// complete weekly or monthly period, as T3 Code decided; otherwise it is unknown.
// A team principal has no config at all. productUsage splits one pool, so it is
// not read as separate quotas.
func parseXAIUsage(root gjson.Result, now time.Time) dynaUsage {
	var usage dynaUsage
	config := root.Get("config")
	if !config.IsObject() {
		return usage
	}
	period := config.Get("currentPeriod")
	seconds := xaiPeriodSeconds(period.Get("type").String())
	end := dynaTime(period.Get("end"))
	if end.IsZero() {
		end = dynaTime(config.Get("billingPeriodEnd"))
	}
	used, ok := dynaPercent(config.Get("creditUsagePercent"))
	if !ok {
		limit := config.Get("monthlyLimit.val").Float()
		switch {
		case limit > 0 && config.Get("used").IsObject():
			used, ok = config.Get("used.val").Float()/limit*100, true
			if seconds == 0 {
				seconds = dynaMonthSeconds
			}
		case seconds != 0 && xaiPeriodComplete(period):
			used, ok = 0, true
		}
	}
	if ok {
		usage.addWindow(seconds, "", used, end, now)
	}
	return usage
}

func xaiPeriodSeconds(periodType string) int64 {
	switch strings.TrimPrefix(periodType, "USAGE_PERIOD_TYPE_") {
	case "WEEKLY":
		return dynaWeekSeconds
	case "MONTHLY":
		return dynaMonthSeconds
	default:
		return 0
	}
}

func xaiPeriodComplete(period gjson.Result) bool {
	start, end := period.Get("start"), period.Get("end")
	if start.Type != gjson.String || end.Type != gjson.String {
		return false
	}
	startAt, endAt := dynaTime(start), dynaTime(end)
	return !startAt.IsZero() && startAt.Before(endAt)
}
