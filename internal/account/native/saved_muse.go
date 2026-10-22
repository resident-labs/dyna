package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
)

// ErrUsageIdle means Muse omitted quota while the five-hour window is idle.
var ErrUsageIdle = errors.New("Muse’s five-hour usage window is idle.")

// Muse 1.4.10 mints a key at startup with this request. The response can
// include subs_usage; MSP usage/read only reads the host’s cached observation.
// This probe discards the minted key and never changes the saved login.
func (*museManager) ProbeSaved(ctx context.Context, c Captured) (store.Observation, error) {
	var o store.Observation
	if err := ctx.Err(); err != nil {
		return o, err
	}
	if c.Credential.Schema != museAuthSchema {
		return o, ErrSchema
	}
	var payload struct {
		Meta json.RawMessage `json:"metaCredential"`
	}
	if json.Unmarshal(c.Credential.Payload, &payload) != nil {
		return o, ErrSchema
	}
	identity, _, expiry, err := museMetaIdentity(payload.Meta)
	if err != nil {
		return o, err
	}
	if identity != c.Identity || c.Integration != "" {
		return o, ErrNoIdentity
	}
	var credential struct {
		AccessToken string `json:"access_token"`
		APIKey      string `json:"api_key"`
	}
	if json.Unmarshal(payload.Meta, &credential) != nil {
		return o, ErrSchema
	}
	if credential.AccessToken == "" {
		return o, ErrUnsupported
	}
	now := time.Now().UTC()
	usageAuthError := func() error {
		if credential.APIKey != "" {
			return errors.New("Muse quota access expired. The saved API key can still run Muse.")
		}
		return ErrInvalidGrant
	}
	if !expiry.IsZero() && !expiry.After(now) {
		return o, usageAuthError()
	}
	body, err := directRequest(ctx, nil, http.MethodPost, "https://api.meta.ai/muse-code/key", `{"onboard":false}`, map[string]string{"Authorization": "Bearer " + credential.AccessToken, "x-api-version": "1.0.0", "Content-Type": "application/json", "Accept": "*/*", "User-Agent": "muse-build/0.1.0"}, now)
	if err != nil {
		if errors.Is(err, ErrInvalidGrant) {
			return o, usageAuthError()
		}
		return o, err
	}
	var response struct {
		Email    string `json:"user_email"`
		TierID   string `json:"subs_tier_id"`
		TierName string `json:"subs_tier_name"`
		Active   bool   `json:"is_subs_active"`
		Usage    *struct {
			Window *struct {
				Percent  *float64 `json:"used_percent"`
				Duration *int64   `json:"window_duration_mins"`
				Reset    *int64   `json:"resets_at"`
			} `json:"window"`
			Weekly *struct {
				Percent *float64 `json:"used_percent"`
				Reset   *int64   `json:"resets_at"`
			} `json:"weekly"`
		} `json:"subs_usage"`
	}
	if json.Unmarshal(body, &response) != nil {
		return o, errors.New("Muse usage response could not be read.")
	}
	if strings.TrimSpace(response.Email) == "" || "meta:email:"+strings.ToLower(strings.TrimSpace(response.Email)) != c.Identity {
		return o, ErrNoIdentity
	}
	o.ObservedAt = now
	if response.TierID != "" || response.TierName != "" {
		o.Plan = &store.Plan{ID: response.TierID, Label: response.TierName}
	}
	add := func(id, label, period string, percent *float64, reset *int64) error {
		if (percent != nil && *percent < 0) || reset == nil || *reset <= 0 {
			return errors.New("Muse usage response has an invalid window.")
		}
		window := store.Window{ID: id, Label: label, Period: period, UsedPercent: percent, Unit: "percent", ResetsAt: time.Unix(*reset, 0).UTC().Format(time.RFC3339)}
		if percent == nil {
			window.UnavailableReason = "Muse reports the reset time without a usage percentage."
		}
		o.Windows = append(o.Windows, window)
		return nil
	}
	if response.Usage == nil {
		if !response.Active {
			return o, errors.New("Muse account has no active subscription usage.")
		}
		o.ObservedAt = time.Time{}
		return o, ErrUsageIdle
	}
	if w := response.Usage.Window; w != nil {
		if w.Duration == nil || *w.Duration <= 0 {
			return store.Observation{}, errors.New("Muse usage response has an invalid window duration.")
		}
		label := fmt.Sprintf("Session (%d minutes)", *w.Duration)
		if *w.Duration%60 == 0 {
			label = fmt.Sprintf("Session (%d-hour)", *w.Duration/60)
		}
		if err := add("subscription", label, "", w.Percent, w.Reset); err != nil {
			return store.Observation{}, err
		}
	}
	if w := response.Usage.Weekly; w != nil {
		if err := add("weekly", "Weekly (7-day)", "week", w.Percent, w.Reset); err != nil {
			return store.Observation{}, err
		}
	}
	if len(o.Windows) == 0 {
		return store.Observation{}, errors.New("Muse usage response has no windows.")
	}
	return o, nil
}
