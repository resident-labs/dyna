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
var errKimiCodeUsageConflict = errors.New("Kimi Code returned conflicting usage windows")

// Count fields can be rounded to whole percentage points. Allow that rounding
// precision, but no more: a 100% versus 0% conflict remains an error.
const kimiCodeRatioTolerance = 0.005

type kimiCodeUsageEntry struct {
	UsedRatio json.RawMessage `json:"used_ratio"`
	ResetTime string          `json:"reset_time"`
}

type kimiCodeCountUsage struct {
	Used      json.RawMessage `json:"used"`
	Limit     json.RawMessage `json:"limit"`
	ResetTime string          `json:"resetTime"`
}

type kimiCodeLimitDetail struct {
	Limit     json.RawMessage `json:"limit"`
	Remaining json.RawMessage `json:"remaining"`
	ResetTime string          `json:"resetTime"`
}

type kimiCodeUsageResponse struct {
	Usage  *kimiCodeCountUsage `json:"usage"`
	Limits []struct {
		Detail *kimiCodeLimitDetail `json:"detail"`
	} `json:"limits"`
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

type kimiCodeUsageRow struct {
	id, label, period string
	entry             *kimiCodeUsageEntry
}

func kimiCodeUsageRows(response kimiCodeUsageResponse) []kimiCodeUsageRow {
	return []kimiCodeUsageRow{
		{"five_hour", "5-hour", "", response.Usages.Limit5h},
		{"seven_day", "Weekly", "week", response.Usages.Limit7d},
		{"monthly", "Monthly", "month", response.Usages.LimitMonthAll},
		{"monthly_code", "Monthly Code", "month", response.Usages.LimitMonthCode},
	}
}

func kimiCodeUsageObservation(raw []byte, now time.Time) (store.Observation, error) {
	var response kimiCodeUsageResponse
	if json.Unmarshal(raw, &response) != nil {
		return store.Observation{}, ErrSchema
	}
	if err := kimiCodeReconcileCounts(response); err != nil {
		return store.Observation{}, err
	}
	windows := []store.Window{}
	for _, row := range kimiCodeUsageRows(response) {
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

type kimiCodeCountWindow struct {
	used, limit int64
	resetTime   string
}

func kimiCodeReconcileCounts(response kimiCodeUsageResponse) error {
	counts := []kimiCodeCountWindow{}
	if response.Usage != nil {
		used, okUsed := kimiCodeNumber(response.Usage.Used)
		limit, okLimit := kimiCodeNumber(response.Usage.Limit)
		if !okUsed || !okLimit {
			return ErrSchema
		}
		counts = append(counts, kimiCodeCountWindow{used: used, limit: limit, resetTime: response.Usage.ResetTime})
	}
	for _, item := range response.Limits {
		if item.Detail == nil {
			continue
		}
		limit, okLimit := kimiCodeNumber(item.Detail.Limit)
		remaining, okRemaining := kimiCodeNumber(item.Detail.Remaining)
		if !okLimit || !okRemaining || remaining > limit {
			return ErrSchema
		}
		counts = append(counts, kimiCodeCountWindow{used: limit - remaining, limit: limit, resetTime: item.Detail.ResetTime})
	}
	for _, count := range counts {
		expected, err := count.ratio()
		if err != nil {
			return err
		}
		for _, row := range kimiCodeUsageRows(response) {
			if row.entry == nil || !kimiResetClose(count.resetTime, row.entry.ResetTime) {
				continue
			}
			ratio, ok := kimiCodeRatio(row.entry.UsedRatio)
			if !ok {
				continue
			}
			if math.Abs(ratio-expected) > kimiCodeRatioTolerance {
				return errKimiCodeUsageConflict
			}
		}
	}
	return nil
}

func (c kimiCodeCountWindow) ratio() (float64, error) {
	if c.used < 0 || c.limit < 0 || c.used > c.limit {
		return 0, ErrSchema
	}
	if c.limit == 0 {
		if c.used != 0 {
			return 0, errKimiCodeUsageConflict
		}
		return 0, nil
	}
	return float64(c.used) / float64(c.limit), nil
}

func kimiCodeNumber(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > math.MaxInt64 || math.Trunc(number) != number {
			return 0, false
		}
		return int64(number), true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	integer, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return integer, true
}

func kimiResetClose(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	a, errA := time.Parse(time.RFC3339Nano, left)
	b, errB := time.Parse(time.RFC3339Nano, right)
	if errA != nil || errB != nil {
		return false
	}
	delta := a.Sub(b)
	if delta < 0 {
		delta = -delta
	}
	return delta <= 5*time.Minute
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
