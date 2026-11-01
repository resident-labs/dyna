package management

import (
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// requestCodexUsage follows openai/codex backend-client. An imported account has
// account_id in metadata; a signed-in one can also carry it in id_token.
func requestCodexUsage(auth *coreauth.Auth) (string, http.Header) {
	token := dynaAccessToken(auth)
	accountID := stringValue(auth.Metadata, "account_id")
	if accountID == "" {
		if claims, errParse := codex.ParseJWTToken(stringValue(auth.Metadata, "id_token")); errParse == nil {
			accountID = claims.GetAccountID()
		}
	}
	if token == "" || accountID == "" {
		return "", nil
	}
	header := dynaBearer(token)
	header.Set("Chatgpt-Account-Id", accountID)
	header.Set("User-Agent", executor.DynaCodexUserAgent())
	return "https://chatgpt.com/backend-api/wham/usage", header
}

func parseCodexUsage(root gjson.Result, now time.Time) dynaUsage {
	var usage dynaUsage
	usage.addCodexLimit(root.Get("rate_limit"), "", now)
	// The backend has sent this as an array and as an object keyed by limit.
	root.Get("additional_rate_limits").ForEach(func(key, limit gjson.Result) bool {
		scope := limit.Get("limit_name").String()
		if scope == "" {
			scope = limit.Get("metered_feature").String()
		}
		if scope == "" && key.Type == gjson.String {
			scope = key.String()
		}
		if scope != "" {
			usage.addCodexLimit(limit.Get("rate_limit"), scope, now)
		}
		return true
	})
	return usage
}

// addCodexLimit classifies a window by its length: primary is weekly on some plans.
func (u *dynaUsage) addCodexLimit(limit gjson.Result, scope string, now time.Time) {
	for _, key := range []string{"primary_window", "secondary_window"} {
		window := limit.Get(key)
		used, ok := dynaPercent(window.Get("used_percent"))
		if !ok {
			continue
		}
		reset := dynaTime(window.Get("reset_at"))
		if after := window.Get("reset_after_seconds"); reset.IsZero() && after.Type == gjson.Number {
			reset = now.Add(time.Duration(after.Int()) * time.Second)
		}
		u.addWindow(window.Get("limit_window_seconds").Int(), scope, used, reset, now)
	}
}
