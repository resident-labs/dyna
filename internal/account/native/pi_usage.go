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

// piNativeFilesSchema is the credential schema of a captured Pi account. The
// payload holds the files of the account's own Pi home, including auth.json.
const piNativeFilesSchema = "pi/native-files@1"

// UsageUnavailableError is a typed usage result. Reason classifies why the
// reading is unavailable; Detail is one sentence that names the provider. It
// never contains a token or a provider response body.
type UsageUnavailableError struct {
	Reason store.UsageReason
	Detail string
}

func (e *UsageUnavailableError) Error() string { return e.Detail }

// piAuthEntry is one provider entry of Pi's auth.json. A reader uses these
// fields only and never writes the entry.
type piAuthEntry struct {
	Type      string `json:"type"`
	Key       string `json:"key"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"` // milliseconds since the Unix epoch
	AccountID string `json:"accountId"`
}

// ProbePiSaved reads one Pi provider account's usage from the credential
// captured from its own Pi home. It never refreshes or writes a credential.
func ProbePiSaved(ctx context.Context, c Captured) (store.Observation, error) {
	return probePiSaved(ctx, c, nil, time.Now().UTC())
}

// probePiSaved runs the reader of the one provider the Pi account holds.
func probePiSaved(ctx context.Context, c Captured, client claudeHTTPClient, now time.Time) (store.Observation, error) {
	empty := store.Observation{}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if c.Integration != "" || c.Credential.Schema != piNativeFilesSchema || len(c.Credential.Payload) == 0 || len(c.Credential.Payload) > 4<<20 {
		return empty, ErrSchema
	}
	var files map[string]json.RawMessage
	if json.Unmarshal(c.Credential.Payload, &files) != nil {
		return empty, ErrSchema
	}
	var auth map[string]piAuthEntry
	if json.Unmarshal(files["auth.json"], &auth) != nil || len(auth) != 1 {
		return empty, ErrSchema
	}
	for id, entry := range auth {
		if c.Identity != "pi:"+id {
			return empty, ErrNoIdentity
		}
		return piProviderUsage(ctx, id, entry, client, now)
	}
	return empty, ErrSchema
}

// piProviderUsage sends the entry to the reader of its provider. A reader takes
// only the credential fields it needs, so another harness can use the same
// reader for an account of that provider.
func piProviderUsage(ctx context.Context, id string, entry piAuthEntry, client claudeHTTPClient, now time.Time) (store.Observation, error) {
	switch id {
	case "openai-codex", "openai", "chatgpt":
		if entry.Type != "oauth" || entry.Access == "" || entry.AccountID == "" {
			return store.Observation{}, piCredentialKind("ChatGPT", entry.Type)
		}
		o, err := probeChatGPTUsage(ctx, client, now, entry.Access, entry.AccountID)
		return piClassify("ChatGPT", o, err)
	case "kimi-coding", "kimi":
		if entry.Type != "oauth" || entry.Access == "" {
			return store.Observation{}, piCredentialKind("Kimi Code", entry.Type)
		}
		o, err := probeKimiCodeUsage(ctx, client, now, "https://api.kimi.com/coding/v1/usages", entry.Access)
		return piClassify("Kimi Code", o, err)
	case "xai":
		if entry.Type != "oauth" || entry.Access == "" {
			return store.Observation{}, piCredentialKind("xAI", entry.Type)
		}
		o, err := probeGrokCredits(ctx, client, now, entry.Access, piXAIUserID(entry.Access))
		return piClassify("xAI", o, err)
	case "opencode-go", "opencode":
		if entry.Type != "api_key" || entry.Key == "" {
			return store.Observation{}, piCredentialKind("OpenCode Go", entry.Type)
		}
		o, err := probeOpenCodeGoUsage(ctx, client, now, entry.Key)
		return piClassify("OpenCode Go", o, err)
	case "deepseek":
		if entry.Type != "api_key" || entry.Key == "" {
			return store.Observation{}, piCredentialKind("DeepSeek", entry.Type)
		}
		o, err := probeDeepSeekBalance(ctx, client, now, entry.Key)
		return piClassify("DeepSeek", o, err)
	case "zai":
		if entry.Type != "api_key" || entry.Key == "" {
			return store.Observation{}, piCredentialKind("z.ai", entry.Type)
		}
		o, err := probeZAIUsage(ctx, client, now, entry.Key)
		return piClassify("z.ai", o, err)
	case "meta", "meta-muse", "muse":
		// Pi 1.4.0's pi-ai auth/oauth/meta.js stores the Meta identity token
		// as refresh and the minted inference key as access: Meta splits
		// identity from inference access, and the standard OAuth scheduler
		// re-mints the key when it expires. The Muse key-mint endpoint reads
		// the plan from the identity token, so an OAuth sign-in can show usage.
		// The entry's expires field is the minted key's expiry, not the identity
		// token's, so a local expiry check would be wrong; a 401 from the
		// endpoint is the reliable signal.
		if entry.Type == "oauth" && entry.Refresh != "" {
			body, err := museUsageRequest(ctx, client, now, entry.Refresh)
			if err != nil {
				return piClassify("Muse", store.Observation{}, err)
			}
			o, err := museUsageObservation(body, now, "")
			return piClassify("Muse", o, err)
		}
		return store.Observation{}, &UsageUnavailableError{
			Reason: store.UsageReasonNotReadable,
			Detail: "Dyna cannot read Muse plan usage from the Pi API key. Sign in to Muse through Pi to read it.",
		}
	case "anthropic":
		return store.Observation{}, &UsageUnavailableError{
			Reason: store.UsageReasonNotReadable,
			Detail: "Dyna has no Anthropic usage reader for a Pi API key.",
		}
	}
	return store.Observation{}, &UsageUnavailableError{
		Reason: store.UsageReasonNotReadable,
		Detail: "Dyna has no usage reader for the " + id + " provider.",
	}
}

// piCredentialKind names a credential whose kind cannot read its plan usage.
func piCredentialKind(provider, kind string) error {
	if kind == "api_key" {
		return &UsageUnavailableError{
			Reason: store.UsageReasonNotReadable,
			Detail: provider + " publishes plan usage only for a sign-in, not for this API key.",
		}
	}
	return &UsageUnavailableError{
		Reason: store.UsageReasonNotReadable,
		Detail: "The saved " + provider + " credential cannot read plan usage. Sign in to " + provider + " again.",
	}
}

// piClassify turns a provider result into a typed, safe result. A provider
// rejection of the saved sign-in is a reading problem, not a sign-out: Pi
// renews the sign-in on its next turn with that provider.
func piClassify(provider string, o store.Observation, err error) (store.Observation, error) {
	if err == nil {
		return o, nil
	}
	// A temporary provider delay keeps its backoff, so the service records
	// when the next attempt may run.
	var quota *QuotaError
	if errors.As(err, &quota) {
		return store.Observation{}, err
	}
	var status *UsageHTTPError
	if errors.As(err, &status) {
		switch {
		case status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden:
			return store.Observation{}, &UsageUnavailableError{
				Reason: store.UsageReasonExpired,
				Detail: "The saved " + provider + " sign-in expired. Sign in to this account again.",
			}
		case status.Status == http.StatusNotFound || status.Status == http.StatusMethodNotAllowed:
			return store.Observation{}, &UsageUnavailableError{
				Reason: store.UsageReasonNotReadable,
				Detail: provider + " does not publish a usage endpoint that Dyna can read.",
			}
		}
		return store.Observation{}, &UsageUnavailableError{
			Reason: store.UsageReasonProviderError,
			Detail: "The " + provider + " usage check failed (HTTP " + strconv.Itoa(status.Status) + ").",
		}
	}
	if errors.Is(err, ErrInvalidGrant) {
		return store.Observation{}, &UsageUnavailableError{
			Reason: store.UsageReasonExpired,
			Detail: "The saved " + provider + " sign-in expired. Sign in to this account again.",
		}
	}
	if errors.Is(err, ErrUsageIdle) {
		// The service keeps the last reading and marks it idle.
		return o, err
	}
	if errors.Is(err, ErrUnsupported) {
		return store.Observation{}, &UsageUnavailableError{
			Reason: store.UsageReasonNotReadable,
			Detail: provider + " does not publish usage that Dyna can read for this credential.",
		}
	}
	if errors.Is(err, ErrSchema) {
		return store.Observation{}, &UsageUnavailableError{
			Reason: store.UsageReasonProviderError,
			Detail: "The " + provider + " usage response is not in a shape Dyna can read.",
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return store.Observation{}, err
	}
	return store.Observation{}, &UsageUnavailableError{
		Reason: store.UsageReasonProviderError,
		Detail: "The " + provider + " usage check failed. Try again later.",
	}
}

// piXAIUserID reads the xAI user ID from the access token when it is a JWT.
// The Grok billing request sends it as x-userid. An unreadable token leaves it
// empty; the request then carries no user header.
func piXAIUserID(token string) string {
	claims, err := jwtClaims(token)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(jsonString(claims["sub"]))
}

// probeOpenCodeGoUsage reads the Go plan with an OpenCode API key.
func probeOpenCodeGoUsage(ctx context.Context, client claudeHTTPClient, now time.Time, key string) (store.Observation, error) {
	raw, err := openCodeUsageRequest(ctx, client, now, "/inference/go/v1/usage", key, "")
	if err != nil {
		return store.Observation{}, err
	}
	return openCodeKeyUsageObservation(raw, now)
}

// probeDeepSeekBalance reads the account balance, which is what DeepSeek
// reports instead of usage windows.
func probeDeepSeekBalance(ctx context.Context, client claudeHTTPClient, now time.Time, key string) (store.Observation, error) {
	raw, err := directRequest(ctx, client, http.MethodGet, "https://api.deepseek.com/user/balance", "", map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}, now)
	if err != nil {
		return store.Observation{}, err
	}
	return deepSeekBalanceObservation(raw, now)
}

func deepSeekBalanceObservation(raw []byte, now time.Time) (store.Observation, error) {
	var response struct {
		Available bool `json:"is_available"`
		Balances  []struct {
			Currency string `json:"currency"`
			Total    string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return store.Observation{}, ErrSchema
	}
	windows := []store.Window{}
	for _, balance := range response.Balances {
		currency := strings.ToUpper(strings.TrimSpace(balance.Currency))
		if currency == "" {
			return store.Observation{}, ErrSchema
		}
		amount, err := strconv.ParseFloat(strings.TrimSpace(balance.Total), 64)
		if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
			return store.Observation{}, ErrSchema
		}
		windows = append(windows, store.Window{
			ID:      "balance-" + strings.ToLower(currency),
			Label:   "Balance (" + currency + ")",
			Balance: &amount,
			Unit:    currency,
		})
	}
	if len(windows) == 0 {
		return store.Observation{}, ErrSchema
	}
	return store.Observation{ObservedAt: now.UTC(), Windows: windows}, nil
}

// probeZAIUsage reads the GLM Coding Plan limits that z.ai publishes for its
// coding-plan API key.
func probeZAIUsage(ctx context.Context, client claudeHTTPClient, now time.Time, key string) (store.Observation, error) {
	raw, err := directRequest(ctx, client, http.MethodGet, "https://api.z.ai/api/monitor/usage/quota/limit", "", map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}, now)
	if err != nil {
		return store.Observation{}, err
	}
	return zaiUsageObservation(raw, now)
}

func zaiUsageObservation(raw []byte, now time.Time) (store.Observation, error) {
	var response struct {
		Code    int  `json:"code"`
		Success bool `json:"success"`
		Data    struct {
			Level  string `json:"level"`
			Limits []struct {
				Type          string   `json:"type"`
				Unit          int      `json:"unit"`
				Number        int      `json:"number"`
				Usage         *float64 `json:"usage"`
				CurrentValue  *float64 `json:"currentValue"`
				Remaining     *float64 `json:"remaining"`
				Percentage    *float64 `json:"percentage"`
				NextResetTime int64    `json:"nextResetTime"`
			} `json:"limits"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Code != http.StatusOK || !response.Success {
		return store.Observation{}, ErrSchema
	}
	observation := store.Observation{ObservedAt: now.UTC(), Windows: []store.Window{}}
	if response.Data.Level != "" {
		observation.Plan = &store.Plan{ID: "glm-coding-" + response.Data.Level, Label: "GLM Coding Plan (" + response.Data.Level + ")"}
	}
	for _, limit := range response.Data.Limits {
		if limit.Percentage == nil || math.IsNaN(*limit.Percentage) || math.IsInf(*limit.Percentage, 0) || *limit.Percentage < 0 || *limit.Percentage > 100 {
			return store.Observation{}, ErrSchema
		}
		reset := ""
		if limit.NextResetTime > 0 {
			reset = time.UnixMilli(limit.NextResetTime).UTC().Format(time.RFC3339)
		}
		window := store.Window{UsedPercent: limit.Percentage, Unit: "percent", ResetsAt: reset}
		switch limit.Type {
		case "TOKENS_LIMIT":
			window.ID, window.Label = "five_hour", "5-hour"
		case "TIME_LIMIT":
			window.ID, window.Label = "monthly_tools", "Search and tools (monthly)"
			window.Used, window.Limit, window.Unit = limit.CurrentValue, limit.Usage, "calls"
			if limit.CurrentValue == nil || limit.Usage == nil || *limit.CurrentValue < 0 || *limit.Usage < 0 {
				return store.Observation{}, ErrSchema
			}
		default:
			window.ID, window.Label = "limit-"+strings.ToLower(limit.Type), "Plan limit"
		}
		observation.Windows = append(observation.Windows, window)
	}
	if len(observation.Windows) == 0 {
		return store.Observation{}, ErrSchema
	}
	return observation, nil
}

// piEntryExpiry reads the access-token expiry of a Pi provider entry, which Pi
// writes in milliseconds. An entry without one keeps no expiry.
func piEntryExpiry(entry piAuthEntry) time.Time {
	if entry.Expires <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(entry.Expires).UTC()
}

// piAccountEntry returns the provider ID and entry of a saved Pi account.
func piAccountEntry(c store.Credential) (string, piAuthEntry, error) {
	if c.Schema != piNativeFilesSchema {
		return "", piAuthEntry{}, ErrSchema
	}
	var files, auth map[string]json.RawMessage
	if json.Unmarshal(c.Payload, &files) != nil || json.Unmarshal(files["auth.json"], &auth) != nil || len(auth) != 1 {
		return "", piAuthEntry{}, ErrSchema
	}
	for id, raw := range auth {
		var entry piAuthEntry
		if json.Unmarshal(raw, &entry) != nil {
			return "", piAuthEntry{}, ErrSchema
		}
		return id, entry, nil
	}
	return "", piAuthEntry{}, ErrSchema
}


// mustJSON encodes v, or an empty object when it cannot be encoded.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// PiGrant returns the provider ID, access token, refresh token and expiry of a
// saved Pi credential. Callers must never log or store the token values.
func PiGrant(c store.Credential) (id, access, refresh string, expiry time.Time, err error) {
	id, entry, err := piAccountEntry(c)
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	return id, entry.Access, entry.Refresh, piEntryExpiry(entry), nil
}

// PiEntry returns the provider ID and raw entry of a saved Pi credential.
func PiEntry(c store.Credential) (string, json.RawMessage, error) {
	if c.Schema != piNativeFilesSchema {
		return "", nil, ErrSchema
	}
	var files, auth map[string]json.RawMessage
	if json.Unmarshal(c.Payload, &files) != nil || json.Unmarshal(files["auth.json"], &auth) != nil || len(auth) != 1 {
		return "", nil, ErrSchema
	}
	for id, entry := range auth {
		return id, entry, nil
	}
	return "", nil, ErrSchema
}

// PiCapturedFromAuth builds the captured login of the one provider an account's
// auth.json holds.
func PiCapturedFromAuth(auth map[string]json.RawMessage) (Captured, error) {
	if len(auth) != 1 {
		return Captured{}, ErrSchema
	}
	for id, entry := range auth {
		var parsed piAuthEntry
		if json.Unmarshal(entry, &parsed) != nil {
			return Captured{}, ErrSchema
		}
		payload, err := json.Marshal(map[string]json.RawMessage{"auth.json": json.RawMessage(mustJSON(map[string]json.RawMessage{id: entry}))})
		if err != nil {
			return Captured{}, ErrSchema
		}
		return Captured{Identity: "pi:" + id, ExpiresAt: piEntryExpiry(parsed), Credential: store.Credential{Schema: piNativeFilesSchema, Payload: payload}}, nil
	}
	return Captured{}, ErrSchema
}

// PiOAuthGrantUsable reports whether a saved Pi account holds an OAuth grant Pi
// can renew. Dyna asks Pi for the renewal; it never runs a provider refresh
// itself.
func PiOAuthGrantUsable(c Captured) bool {
	_, entry, err := piAccountEntry(c.Credential)
	return err == nil && entry.Type == "oauth" && entry.Access != "" && entry.Refresh != ""
}
