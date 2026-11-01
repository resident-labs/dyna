package management

import (
	"net/http"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// requestOpenCodeUsage follows anomalyco/opencode usage-limits.ts. The workspace
// header it sends belongs to Console sign-ins, not to a Go key.
func requestOpenCodeUsage(auth *coreauth.Auth) (string, http.Header) {
	key := dynaAPIKey(auth)
	if key == "" {
		return "", nil
	}
	return "https://opencode.ai/zen/go/v1/usage", dynaBearer(key)
}

// parseOpenCodeUsage treats rate-limited as spent. A window at 0% carries a
// placeholder reset, which cc-switch found, so that reset is dropped.
func parseOpenCodeUsage(root gjson.Result, now time.Time) dynaUsage {
	var usage dynaUsage
	for _, window := range []struct {
		key     string
		seconds int64
	}{{"rolling", dynaFiveHourSeconds}, {"weekly", dynaWeekSeconds}, {"monthly", dynaMonthSeconds}} {
		meter := root.Get("usage." + window.key)
		used, ok := dynaPercent(meter.Get("percent"))
		if meter.Get("status").String() == "rate-limited" {
			used, ok = 100, true
		}
		if !ok {
			continue
		}
		reset := dynaTime(meter.Get("resetsAt"))
		if used == 0 {
			reset = time.Time{}
		}
		usage.addWindow(window.seconds, "", used, reset, now)
	}
	return usage
}
