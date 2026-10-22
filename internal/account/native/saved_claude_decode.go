package native

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
)

// DecodeClaudeUsage turns an Anthropic OAuth usage response into windows:
// five_hour and seven_day buckets plus model-scoped entries in limits.
// Open-source clients of this endpoint (ClaudeMeter UsageAPIResponse.swift,
// TokenEater UsageModels.swift, qgrid anthropic-quota.ts) read utilization
// and limits[].percent as percentages from 0 to 100.
func DecodeClaudeUsage(body []byte) ([]store.Window, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, errors.New("Claude usage response has an unknown format.")
	}
	type bucket struct {
		Utilization json.RawMessage `json:"utilization"`
		ResetsAt    json.RawMessage `json:"resets_at"`
	}
	read := func(key string) *bucket {
		var b *bucket
		if json.Unmarshal(payload[key], &b) != nil {
			return nil
		}
		return b
	}
	session := read("five_hour")
	weekly := read("seven_day_oauth_apps")
	if weekly == nil {
		weekly = read("seven_day")
	}
	var scoped []struct {
		Kind     string          `json:"kind"`
		Percent  json.RawMessage `json:"percent"`
		ResetsAt json.RawMessage `json:"resets_at"`
		Scope    struct {
			Model struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
	}
	_ = json.Unmarshal(payload["limits"], &scoped)

	// Units are fixed per field: bucket utilization and limits[].percent are
	// both percentages from 0 to 100. A value is never rescaled from another field.
	percent := func(value json.RawMessage) *float64 {
		n := claudeNumber(value)
		if math.IsNaN(n) || n < 0 {
			return nil
		}
		n = math.Min(n, 100)
		return &n
	}

	windows := []store.Window{}
	if session != nil {
		if p := percent(session.Utilization); p != nil {
			windows = append(windows, store.Window{ID: "five_hour", Label: "Session (5-hour)", UsedPercent: p, Unit: "percent", ResetsAt: claudeReset(session.ResetsAt)})
		}
	}
	if weekly != nil {
		if p := percent(weekly.Utilization); p != nil {
			windows = append(windows, store.Window{ID: "seven_day", Label: "Weekly (7-day)", UsedPercent: p, Unit: "percent", ResetsAt: claudeReset(weekly.ResetsAt)})
		}
	}
	seen := map[string]bool{}
	for _, s := range scoped {
		name := strings.TrimSpace(s.Scope.Model.DisplayName)
		if name == "" {
			name = strings.TrimSpace(s.Scope.Model.ID)
		}
		key := name + "\x00" + s.Kind
		if name == "" || seen[key] {
			continue
		}
		p := percent(s.Percent)
		if p == nil {
			continue
		}
		seen[key] = true
		label := name
		if w := claudeScopedWindow(s.Kind); w != "" {
			label += " " + w
		}
		windows = append(windows, store.Window{ID: "scoped:" + key, Label: label, UsedPercent: p, Unit: "percent", ResetsAt: claudeReset(s.ResetsAt)})
	}
	if len(windows) == 0 {
		return nil, errors.New("Claude usage response has no limits.")
	}
	return windows, nil
}

func claudeScopedWindow(kind string) string {
	k := strings.ToLower(kind)
	switch {
	case strings.Contains(k, "month"):
		return "Monthly"
	case strings.Contains(k, "week"), strings.Contains(k, "day"):
		return "Weekly"
	case strings.Contains(k, "hour"), strings.Contains(k, "session"):
		return "Session"
	}
	return ""
}

func claudeNumber(raw json.RawMessage) float64 {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return math.NaN()
	}
	switch x := v.(type) {
	case float64:
		return x
	case string:
		n, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(x), "%"), 64)
		if err == nil {
			return n
		}
	}
	return math.NaN()
}

// claudeReset accepts ISO 8601 text or Unix seconds or milliseconds.
func claudeReset(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v == nil {
		return ""
	}
	text := strings.TrimSpace(strings.Trim(string(raw), `"`))
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		if n < 1e12 {
			n *= 1000
		}
		return time.UnixMilli(n).UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return text
}
