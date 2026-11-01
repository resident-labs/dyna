package management

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// kimiBoosterScale is the wallet's fixed point: one cent is a million units.
const kimiBoosterScale = 100_000_000

// requestKimiUsage follows MoonshotAI/kimi-code packages/oauth/src/managed-usage.ts.
func requestKimiUsage(auth *coreauth.Auth) (string, http.Header) {
	token := dynaAccessToken(auth)
	if token == "" {
		return "", nil
	}
	return kimi.ResolveKimiAPIBaseURL(stringValue(auth.Metadata, "domain")) + "/v1/usages", dynaBearer(token)
}

type kimiReading struct {
	seconds int64
	used    float64
	reset   time.Time
}

// parseKimiUsage works around official parsing that shows false zeros (kimi-code
// #4133, #3817): used_ratio can be 0 beside legacy counters saying 10 of 100. When
// both cover one duration and reset, the more exhausted reading wins (CodexBar
// #4306). A monthly pool always shows when present.
func parseKimiUsage(root gjson.Result, now time.Time) dynaUsage {
	var usage dynaUsage
	usages := root.Get("usages")
	counters := kimiCounters(root)
	for _, pool := range []struct {
		key     string
		seconds int64
	}{{"limit_5h", dynaFiveHourSeconds}, {"limit_7d", dynaWeekSeconds}} {
		reading, ok := kimiPool(usages.Get(pool.key), pool.seconds)
		counter, counted := kimiTakeCounter(&counters, pool.seconds)
		switch {
		case !ok && counted:
			reading, ok = counter, true
		case ok && counted && reading.reset.Sub(counter.reset).Abs() < time.Minute:
			reading.used = max(reading.used, counter.used)
		}
		if ok {
			usage.addWindow(reading.seconds, "", reading.used, reading.reset, now)
		}
	}
	for _, counter := range counters {
		usage.addWindow(counter.seconds, "", counter.used, counter.reset, now)
	}
	if reading, ok := kimiPool(usages.Get("limit_month_total"), dynaMonthSeconds); ok {
		usage.addWindow(reading.seconds, "", reading.used, reading.reset, now)
	}
	if reading, ok := kimiPool(usages.Get("limit_month_code"), dynaMonthSeconds); ok {
		usage.addWindow(reading.seconds, "Code", reading.used, reading.reset, now)
	}
	if balance, ok := kimiBooster(root.Get("boosterWallet")); ok {
		usage.balances = append(usage.balances, balance)
	}
	return usage
}

func kimiPool(pool gjson.Result, seconds int64) (kimiReading, bool) {
	ratio, ok := dynaPercent(pool.Get("used_ratio"))
	if !ok {
		return kimiReading{}, false
	}
	return kimiReading{seconds: seconds, used: math.Round(ratio*10000) / 100, reset: dynaTime(pool.Get("reset_time"))}, true
}

// kimiCounters reads the legacy counters: limits[] with explicit windows, and
// usage, which kimi-cli labels the weekly limit.
func kimiCounters(root gjson.Result) []kimiReading {
	var counters []kimiReading
	if reading, ok := kimiCounter(root.Get("usage"), dynaWeekSeconds); ok {
		counters = append(counters, reading)
	}
	for _, limit := range root.Get("limits").Array() {
		seconds := kimiWindowSeconds(limit.Get("window"))
		if seconds == 0 {
			continue
		}
		if reading, ok := kimiCounter(limit.Get("detail"), seconds); ok {
			counters = append(counters, reading)
		}
	}
	return counters
}

func kimiCounter(detail gjson.Result, seconds int64) (kimiReading, bool) {
	limit, ok := dynaPercent(detail.Get("limit"))
	if !ok || limit <= 0 {
		return kimiReading{}, false
	}
	used, ok := dynaPercent(detail.Get("used"))
	if !ok {
		remaining, known := dynaPercent(detail.Get("remaining"))
		if !known || remaining > limit {
			return kimiReading{}, false
		}
		used = limit - remaining
	}
	reset := time.Time{}
	for _, key := range []string{"resetTime", "reset_time", "resetAt", "reset_at"} {
		if reset = dynaTime(detail.Get(key)); !reset.IsZero() {
			break
		}
	}
	return kimiReading{seconds: seconds, used: used / limit * 100, reset: reset}, true
}

func kimiWindowSeconds(window gjson.Result) int64 {
	duration := window.Get("duration").Int()
	switch strings.TrimPrefix(strings.ToUpper(window.Get("timeUnit").String()), "TIME_UNIT_") {
	case "SECOND":
		return duration
	case "MINUTE":
		return duration * 60
	case "HOUR":
		return duration * 3600
	case "DAY":
		return duration * 86400
	default:
		return 0
	}
}

func kimiTakeCounter(counters *[]kimiReading, seconds int64) (kimiReading, bool) {
	for i, counter := range *counters {
		if counter.seconds == seconds {
			*counters = append((*counters)[:i], (*counters)[i+1:]...)
			return counter, true
		}
	}
	return kimiReading{}, false
}

func kimiBooster(wallet gjson.Result) (dynaQuotaBalance, bool) {
	balance := wallet.Get("balance")
	amount, ok := dynaPercent(balance.Get("amount"))
	if balance.Get("type").String() != "BOOSTER" || !ok || amount <= 0 {
		return dynaQuotaBalance{}, false
	}
	left, ok := dynaPercent(balance.Get("amountLeft"))
	if !ok {
		left = 0
	}
	currency := wallet.Get("monthlyChargeLimit.currency").String()
	if currency == "" {
		currency = wallet.Get("monthlyUsed.currency").String()
	}
	if currency == "" {
		currency = "USD"
	}
	return dynaQuotaBalance{Currency: currency, Amount: strconv.FormatFloat(left/kimiBoosterScale, 'f', 2, 64)}, true
}
