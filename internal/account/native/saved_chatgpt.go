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

// ChatGPTTokens returns the access token and ChatGPT account ID of a saved Pi
// account that signed in to OpenAI. Callers must never log or store the values.
func ChatGPTTokens(c store.Credential) (access, account string, err error) {
	if c.Schema != piNativeFilesSchema {
		return "", "", ErrSchema
	}
	var files, auth map[string]json.RawMessage
	if json.Unmarshal(c.Payload, &files) != nil || json.Unmarshal(files["auth.json"], &auth) != nil || len(auth) != 1 {
		return "", "", ErrSchema
	}
	for _, entry := range auth {
		var value piAuthEntry
		if json.Unmarshal(entry, &value) != nil || value.Access == "" || value.AccountID == "" {
			return "", "", ErrSchema
		}
		return value.Access, value.AccountID, nil
	}
	return "", "", ErrSchema
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
		observation.Windows = append(observation.Windows, store.Window{ID: id, Label: label, UsedPercent: window.UsedPercent, Unit: "percent", ResetsAt: time.Unix(window.Reset, 0).UTC().Format(time.RFC3339)})
	}
	if len(observation.Windows) == 0 {
		return store.Observation{}, schemaErr
	}
	return observation, nil
}
