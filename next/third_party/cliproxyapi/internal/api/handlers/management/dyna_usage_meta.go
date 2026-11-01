package management

import (
	"strconv"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// observedMetaUsage reads the signals Meta's response.subscription_usage leaves on
// the credential. Meta has no usage endpoint, so an account shows nothing until it
// serves a request, and a window past its reset is stale, not empty.
func observedMetaUsage(auth *coreauth.Auth, now time.Time) dynaUsage {
	var usage dynaUsage
	signals := auth.Quota.Signals
	seconds, errMinutes := strconv.ParseInt(signals[helps.MetaQuotaWindowMinutesHeader], 10, 64)
	if errMinutes == nil && seconds > 0 {
		usage.addMetaWindow(seconds*60, signals[helps.MetaQuotaWindowUsedHeader], signals[helps.MetaQuotaWindowResetHeader], now)
	}
	usage.addMetaWindow(dynaWeekSeconds, signals[helps.MetaQuotaWeeklyUsedHeader], signals[helps.MetaQuotaWeeklyResetHeader], now)
	return usage
}

func (u *dynaUsage) addMetaWindow(seconds int64, used, reset string, now time.Time) {
	percent, errPercent := strconv.ParseFloat(used, 64)
	resetAt, errReset := strconv.ParseInt(reset, 10, 64)
	if errPercent != nil || errReset != nil || percent < 0 || !time.Unix(resetAt, 0).After(now) {
		return
	}
	u.addWindow(seconds, "", percent, time.Unix(resetAt, 0), now)
}
