package management

import (
	"net/http"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func requestDeepSeekUsage(auth *coreauth.Auth) (string, http.Header) {
	key := dynaAPIKey(auth)
	if key == "" {
		return "", nil
	}
	return "https://api.deepseek.com/user/balance", dynaBearer(key)
}

// parseDeepSeekUsage keeps each currency apart. DeepSeek has no limit windows, and
// is_available false means a low balance, not a bad key, so it is not read.
func parseDeepSeekUsage(root gjson.Result, _ time.Time) dynaUsage {
	var usage dynaUsage
	for _, info := range root.Get("balance_infos").Array() {
		currency := info.Get("currency").String()
		total := info.Get("total_balance")
		if _, ok := dynaPercent(total); currency != "" && ok {
			usage.balances = append(usage.balances, dynaQuotaBalance{Currency: currency, Amount: total.String()})
		}
	}
	return usage
}
