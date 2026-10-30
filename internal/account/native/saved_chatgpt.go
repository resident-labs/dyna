package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"dyna-agent/internal/account/store"
)

// probeChatGPTUsage reads the ChatGPT plan with an access token and account
// ID. It takes no harness credential, so any account of the provider can use it.
func probeChatGPTUsage(ctx context.Context, client claudeHTTPClient, now time.Time, access, account string) (store.Observation, error) {
	body, err := directRequest(ctx, client, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", "", map[string]string{"Authorization": "Bearer " + access, "ChatGPT-Account-Id": account, "Accept": "application/json"}, now, http.StatusForbidden)
	if err != nil {
		return store.Observation{}, err
	}
	return chatgptUsageObservation(body, now)
}

type chatgptUsageWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	Duration    int64    `json:"limit_window_seconds"`
	Reset       int64    `json:"reset_at"`
}

// chatgptUsageObservation reads the plan windows of one ChatGPT usage response.
func chatgptUsageObservation(raw []byte, now time.Time) (store.Observation, error) {
	var response struct {
		Plan   string `json:"plan_type"`
		Limits struct {
			Primary   *chatgptUsageWindow `json:"primary_window"`
			Secondary *chatgptUsageWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	schemaErr := errors.New("ChatGPT returned an unsupported usage response.")
	if json.Unmarshal(raw, &response) != nil {
		return store.Observation{}, schemaErr
	}
	observation := store.Observation{ObservedAt: now, Windows: []store.Window{}}
	if response.Plan != "" {
		observation.Plan = &store.Plan{ID: response.Plan, Label: response.Plan}
	}
	for _, part := range []struct {
		name   string
		window *chatgptUsageWindow
	}{{"primary", response.Limits.Primary}, {"secondary", response.Limits.Secondary}} {
		window := part.window
		if window == nil {
			continue
		}
		if window.UsedPercent == nil || *window.UsedPercent < 0 || *window.UsedPercent > 100 || window.Duration <= 0 || window.Reset <= 0 {
			return store.Observation{}, schemaErr
		}
		id, label := part.name, "ChatGPT "+part.name
		switch window.Duration {
		case 18000:
			id, label = "five_hour", "5-hour"
		case 604800:
			id, label = "seven_day", "Weekly"
		}
		reset := time.Unix(window.Reset, 0).UTC().Format(time.RFC3339)
		observation.Windows = append(observation.Windows, store.Window{ID: id, Label: label, UsedPercent: window.UsedPercent, Unit: "percent", ResetsAt: reset, PeriodStart: windowStart(reset, time.Duration(window.Duration)*time.Second)})
	}
	if len(observation.Windows) == 0 {
		return store.Observation{}, schemaErr
	}
	return observation, nil
}
