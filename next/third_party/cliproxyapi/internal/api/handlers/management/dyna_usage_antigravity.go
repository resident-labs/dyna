package management

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func requestAntigravityUsage(auth *coreauth.Auth) (string, http.Header) {
	token := dynaAccessToken(auth)
	if token == "" {
		return "", nil
	}
	header := dynaBearer(token)
	header.Set("Content-Type", "application/json")
	header.Set("User-Agent", misc.AntigravityUserAgent())
	return antigravity.DailyAPIEndpoint + antigravity.ModelsPath, header
}

func antigravityUsageBody(auth *coreauth.Auth) []byte {
	body, _ := json.Marshal(map[string]string{"project": stringValue(auth.Metadata, "project_id")})
	return body
}

// Antigravity reports the remaining fraction and reset for each model, not the period length.
func parseAntigravityUsage(root gjson.Result, now time.Time) dynaUsage {
	var usage dynaUsage
	models := root.Get("models").Map()
	for _, model := range registry.GetAntigravityModels() {
		quota := models[model.ID].Get("quotaInfo")
		remaining, ok := dynaNumber(quota.Get("remainingFraction"))
		if !ok || remaining < 0 || remaining > 1 {
			continue
		}
		reset := dynaTime(quota.Get("resetTime"))
		if !reset.IsZero() && !dynaResetPossible(reset, 0, now) {
			continue
		}
		usage.addWindow(0, antigravity.DynaModelPrefix+"/"+model.ID, 100*(1-remaining), reset, now)
	}
	return usage
}
