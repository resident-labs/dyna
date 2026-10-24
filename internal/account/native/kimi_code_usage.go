package native

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
)

var errKimiCodeUsage = errors.New("Kimi Code returned no usage windows")

type kimiCodeUsageEntry struct {
	UsedRatio json.RawMessage `json:"used_ratio"`
	ResetTime string          `json:"reset_time"`
}

type kimiCodeUsageResponse struct {
	Usages struct {
		Limit5h        *kimiCodeUsageEntry `json:"limit_5h"`
		Limit7d        *kimiCodeUsageEntry `json:"limit_7d"`
		LimitMonthAll  *kimiCodeUsageEntry `json:"limit_month_total"`
		LimitMonthCode *kimiCodeUsageEntry `json:"limit_month_code"`
	} `json:"usages"`
}

// ProbeSaved reads plan usage only for Dyna's native Kimi Code credential.
// It never refreshes a token or reads another harness's credential store.
func (m *kimiCodeManager) ProbeSaved(ctx context.Context, c Captured) (store.Observation, error) {
	empty := store.Observation{}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if c.Identity != kimiCodeIdentity || c.Integration != "" {
		return empty, ErrNoIdentity
	}
	login, err := unpackKimiCodeLogin(c.Credential)
	if err != nil {
		return empty, err
	}
	now := directNow(m.now)
	if !login.expiresAt.After(now) {
		// Keep an older observation stale rather than turning a locally known
		// expiry into a claim that the provider rejected the grant.
		return empty, &UsageHTTPError{Status: http.StatusUnauthorized}
	}
	baseURL := strings.TrimSuffix(login.baseURL, "/")
	if baseURL != "https://api.kimi.com/coding/v1" && baseURL != "https://api.kimi.ai/coding/v1" {
		return empty, ErrUnsupported
	}
	endpoint := m.usageURL
	if endpoint == "" {
		endpoint = baseURL + "/usages"
	}
	raw, err := directRequest(ctx, m.client, http.MethodGet, endpoint, "", map[string]string{
		"Authorization": "Bearer " + login.tokenInfo.AccessToken,
		"Accept":        "application/json",
	}, now)
	if err != nil {
		return empty, err
	}
	return kimiCodeUsageObservation(raw, now)
}

func kimiCodeUsageObservation(raw []byte, now time.Time) (store.Observation, error) {
	var response kimiCodeUsageResponse
	if json.Unmarshal(raw, &response) != nil {
		return store.Observation{}, ErrSchema
	}
	windows := []store.Window{}
	for _, row := range []struct {
		id, label, period string
		entry             *kimiCodeUsageEntry
	}{
		{"five_hour", "5-hour", "", response.Usages.Limit5h},
		{"seven_day", "Weekly", "week", response.Usages.Limit7d},
		{"monthly", "Monthly", "month", response.Usages.LimitMonthAll},
		{"monthly_code", "Monthly Code", "month", response.Usages.LimitMonthCode},
	} {
		if row.entry == nil {
			continue
		}
		ratio, ok := kimiCodeRatio(row.entry.UsedRatio)
		if !ok {
			continue
		}
		percent := ratio * 100
		windows = append(windows, store.Window{ID: row.id, Label: row.label, Period: row.period, UsedPercent: &percent, Unit: "percent", ResetsAt: validReset(row.entry.ResetTime)})
	}
	if len(windows) == 0 {
		return store.Observation{}, errKimiCodeUsage
	}
	return store.Observation{ObservedAt: now.UTC(), Windows: windows}, nil
}

func kimiCodeRatio(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return validKimiRatio(number)
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return validKimiRatio(number)
}

func validKimiRatio(value float64) (float64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return 0, false
	}
	return value, true
}
