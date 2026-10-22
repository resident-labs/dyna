package native

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
)

// ProbeSaved uses the same backend usage request as the Codex CLI, with the
// supplied private account credential. It never refreshes or writes credentials.
func (m *codexManager) ProbeSaved(ctx context.Context, c Captured) (store.Observation, error) {
	raw, captured, err := codexUnpack(c.Credential)
	if err != nil {
		return store.Observation{}, err
	}
	if captured.Identity != c.Identity {
		return store.Observation{}, ErrNoIdentity
	}
	access, account, err := codexTokens(raw)
	if err != nil {
		return store.Observation{}, err
	}
	now := time.Now().UTC()
	var client claudeHTTPClient
	if m.client != nil {
		client = m.client
	}
	body, err := directRequest(ctx, client, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", "", map[string]string{"Authorization": "Bearer " + access, "ChatGPT-Account-Id": account, "Accept": "application/json"}, now, http.StatusForbidden)
	if err != nil {
		return store.Observation{}, err
	}
	return codexUsageObservation(body, now)
}

// CodexTokens returns the access token and ChatGPT account ID of a captured
// Codex login. Callers must never log or store them.
func CodexTokens(c store.Credential) (access, account string, err error) {
	raw, _, err := codexUnpack(c)
	if err != nil {
		return "", "", err
	}
	return codexTokens(raw)
}

func codexTokens(raw []byte) (access, account string, err error) {
	var auth struct {
		Tokens struct {
			Access  string `json:"access_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Tokens.Access == "" {
		return "", "", ErrSchema
	}
	return auth.Tokens.Access, auth.Tokens.Account, nil
}

type codexUsageWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	Duration    int64    `json:"limit_window_seconds"`
	Reset       int64    `json:"reset_at"`
}

func codexUsageObservation(raw []byte, now time.Time) (store.Observation, error) {
	var response struct {
		Plan   string `json:"plan_type"`
		Limits struct {
			Primary   *codexUsageWindow `json:"primary_window"`
			Secondary *codexUsageWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	schemaErr := errors.New("Codex returned an unsupported usage response.")
	if json.Unmarshal(raw, &response) != nil {
		return store.Observation{}, schemaErr
	}
	observation := store.Observation{ObservedAt: now, Windows: []store.Window{}}
	if response.Plan != "" {
		observation.Plan = &store.Plan{ID: response.Plan, Label: response.Plan}
	}
	for _, part := range []struct {
		name   string
		window *codexUsageWindow
	}{{"primary", response.Limits.Primary}, {"secondary", response.Limits.Secondary}} {
		window := part.window
		if window == nil {
			continue
		}
		if window.UsedPercent == nil || *window.UsedPercent < 0 || *window.UsedPercent > 100 || window.Duration <= 0 || window.Reset <= 0 {
			return store.Observation{}, schemaErr
		}
		id, label := part.name, "Codex "+part.name
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

func codexQuotaRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= 86400*365 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at)
		if delay > 0 && delay <= 365*24*time.Hour {
			return delay
		}
	}
	return 0
}
