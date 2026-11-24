package management

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
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

// Antigravity reports the fraction and reset for each model, not a pool ID or period length.
func parseAntigravityUsage(root gjson.Result, now time.Time) dynaUsage {
	type reading struct {
		group     string
		remaining float64
		reset     time.Time
	}
	type pool struct {
		reading
		models      []string
		displayName string
		claude      bool
		gptOSS      bool
	}
	models := root.Get("models").Map()
	var pools []pool
	byReading := make(map[reading]int)
	for _, model := range registry.GetAntigravityModels() {
		quota := models[model.ID].Get("quotaInfo")
		remaining, ok := dynaNumber(quota.Get("remainingFraction"))
		if !ok || remaining < 0 || remaining > 1 {
			continue
		}
		reset := dynaTime(quota.Get("resetTime")).UTC()
		if !reset.IsZero() && !dynaResetPossible(reset, 0, now) {
			continue
		}
		group := cache.GetModelGroup(model.ID)
		isClaude, isGPTOSS := group == "claude", group == "gpt" && strings.HasPrefix(model.ID, "gpt-oss-")
		// Claude and GPT-OSS had identical readings on the live plan. Keep that
		// grouping only while both the unrounded fraction and reset agree.
		if isGPTOSS {
			group = "claude"
		}
		key := reading{group, remaining, reset}
		i, found := byReading[key]
		if !found || reset.IsZero() {
			i = len(pools)
			pools = append(pools, pool{reading: key, displayName: model.DisplayName})
			byReading[key] = i
		}
		p := &pools[i]
		p.models = append(p.models, model.ID)
		p.claude = p.claude || isClaude
		p.gptOSS = p.gptOSS || isGPTOSS
	}

	var usage dynaUsage
	for _, p := range pools {
		scope := p.displayName
		if scope == "" {
			scope = p.models[0]
		}
		if len(p.models) > 1 {
			switch {
			case p.claude && p.gptOSS:
				scope = "Claude and GPT-OSS models"
			case p.claude:
				scope = "Claude models"
			case p.gptOSS:
				scope = "GPT-OSS models"
			case p.group == "gemini":
				scope = "Gemini models"
			case p.group == "gpt":
				scope = "GPT models"
			}
		}
		window := authFileQuotaWindow{Kind: "other", Scope: scope, Utilization: 100 * (1 - p.remaining)}
		window.Name = antigravity.DynaModelPrefix + "/" + p.models[0] + " window_0"
		window.Models = p.models
		if !p.reset.IsZero() {
			window.ResetsAt = p.reset.Format(time.RFC3339Nano)
		}
		usage.windows = append(usage.windows, window)
	}
	return usage
}
