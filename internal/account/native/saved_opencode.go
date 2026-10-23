package native

import (
	"context"
	"dyna-agent/internal/account/store"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"strings"
	"time"
)

var errOpenCodeUsage = errors.New("OpenCode returned an unsupported usage response.")

// OpenCode 2.0.32 embeds this request identity in its native binary.
// Cloudflare rejects the default HTTP client identity with Error 1010.
func (m *openCodeManager) openCodeJSON(ctx context.Context, path, token, orgID string, out any) error {
	headers := map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json", "User-Agent": "opencode/latest/2.0.32/cli"}
	if orgID != "" {
		headers["x-org-id"] = orgID
	}
	raw, err := directRequest(ctx, m.client, http.MethodGet, "https://opencode.ai"+path, "", headers, time.Now().UTC())
	var status *UsageHTTPError
	if errors.As(err, &status) && (status.Status == 404 || status.Status == 405) {
		return ErrUnsupported
	}
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, out) != nil {
		return errOpenCodeUsage
	}
	return nil
}

type openCodePrivateCredential struct {
	ID            string `json:"id"`
	IntegrationID string `json:"integrationID"`
	Value         struct {
		Type     string `json:"type"`
		Key      string `json:"key"`
		Access   string `json:"access"`
		Expires  int64  `json:"expires"`
		Metadata struct {
			Server    string `json:"server"`
			OrgID     string `json:"orgID"`
			AccountID string `json:"accountID"`
			Email     string `json:"email"`
		} `json:"metadata"`
	} `json:"value"`
}

func (m *openCodeManager) readOpenCodeConsoleUsage(ctx context.Context, token, orgID string) ([]store.Window, error) {
	var response struct {
		Access *struct {
			Meters map[string]struct {
				Used     json.RawMessage `json:"usedMicroCents"`
				Limit    json.RawMessage `json:"limitMicroCents"`
				ResetsAt string          `json:"resetsAt"`
			} `json:"meters"`
		} `json:"access"`
	}
	err := m.openCodeJSON(ctx, "/console/api/go/status", token, orgID, &response)
	if err != nil {
		return nil, err
	}
	if response.Access == nil {
		return nil, ErrUnsupported
	}
	var windows []store.Window
	for _, row := range []struct{ id, label string }{{"fiveHour", "Five hours"}, {"week", "Weekly"}, {"month", "Monthly"}} {
		meter, ok := response.Access.Meters[row.id]
		if !ok {
			continue
		}
		used, okU := microUSD(meter.Used)
		limit, okL := microUSD(meter.Limit)
		if !okU || !okL || limit <= 0 {
			continue
		}
		percent := used / limit * 100
		if math.IsNaN(percent) || math.IsInf(percent, 0) {
			continue
		}
		windows = append(windows, store.Window{ID: row.id, Label: row.label, Used: &used, Limit: &limit, Unit: "USD", UsedPercent: &percent, ResetsAt: validReset(meter.ResetsAt)})
	}
	if len(windows) == 0 {
		return nil, errOpenCodeUsage
	}
	return windows, nil
}
func microUSD(raw json.RawMessage) (float64, bool) {
	text := strings.Trim(string(raw), "\"")
	// Console uses nonnegative integer microcent strings (or exact integer JSON
	// numbers). Reject decimal/exponent syntax before any floating projection.
	if len(text) == 0 || len(text) > 128 {
		return 0, false
	}
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	integer, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return 0, false
	}
	amount := new(big.Rat).SetFrac(integer, big.NewInt(100000000))
	projected, _ := amount.Float64()
	if math.IsNaN(projected) || math.IsInf(projected, 0) {
		return 0, false
	}
	return projected, true
}

func validReset(raw string) string {
	if raw == "" {
		return ""
	}
	v, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return ""
	}
	return v.UTC().Format(time.RFC3339Nano)
}
func (m *openCodeManager) readOpenCodeKeyUsage(ctx context.Context, token string) ([]store.Window, error) {
	var response struct {
		Usage map[string]struct {
			Percent  *float64 `json:"percent"`
			ResetsAt string   `json:"resetsAt"`
		} `json:"usage"`
	}
	err := m.openCodeJSON(ctx, "/inference/go/v1/usage", token, "", &response)
	if err != nil {
		return nil, err
	}
	var windows []store.Window
	for _, row := range []struct{ id, label string }{{"rolling", "Five hours"}, {"weekly", "Weekly"}, {"monthly", "Monthly"}} {
		w, ok := response.Usage[row.id]
		if !ok || w.Percent == nil || *w.Percent < 0 || math.IsNaN(*w.Percent) || math.IsInf(*w.Percent, 0) {
			continue
		}
		windows = append(windows, store.Window{ID: row.id, Label: row.label, UsedPercent: w.Percent, ResetsAt: validReset(w.ResetsAt)})
	}
	if len(windows) == 0 {
		return nil, errOpenCodeUsage
	}
	return windows, nil
}

func (m *openCodeManager) usageCredentials(ctx context.Context, s openCodeScope, integration string) ([]openCodePrivateCredential, error) {
	raw, err := m.run(ctx, s, "auth", "export", integration, "--standalone")
	if err != nil {
		return nil, err
	}
	var credentials []openCodePrivateCredential
	if json.Unmarshal(raw, &credentials) != nil {
		return nil, ErrSchema
	}
	return credentials, nil
}

// ProbeSaved resolves the exact credential reference inside the manager's private
// OpenCode scope. OpenCode retains ownership of OAuth token renewal.
func (m *openCodeManager) ProbeSaved(ctx context.Context, c Captured) (store.Observation, error) {
	empty := store.Observation{}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	ref, err := openCodeUnpack(c.Credential)
	if err != nil {
		return empty, err
	}
	s, err := m.scope()
	if err != nil {
		return empty, err
	}
	if !openCodeSameScope(ref.Scope, s) {
		return empty, ErrSchema
	}
	if !store.IsOpenCodeIntegration(ref.Integration) {
		return empty, ErrUnsupported
	}
	if c.Integration != ref.Integration || c.Identity != "opencode:"+ref.Integration+":"+ref.CredentialID {
		return empty, ErrNoIdentity
	}
	credentials, err := m.usageCredentials(ctx, s, ref.Integration)
	if err != nil {
		return empty, err
	}
	for _, credential := range credentials {
		if credential.ID != ref.CredentialID || credential.IntegrationID != ref.Integration {
			continue
		}
		value := credential.Value
		if value.Metadata.Server != "" && value.Metadata.Server != "https://opencode.ai/console" {
			return empty, ErrUnsupported
		}
		var windows []store.Window
		switch {
		case value.Type == "oauth" && value.Access != "":
			if value.Expires != 0 && value.Expires <= time.Now().UnixMilli() {
				return empty, ErrInvalidGrant
			}
			windows, err = m.readOpenCodeConsoleUsage(ctx, value.Access, value.Metadata.OrgID)
		case value.Type == "key" && value.Key != "":
			if ref.Integration == "opencode" {
				windows, err = m.readOpenCodeConsoleUsage(ctx, value.Key, value.Metadata.OrgID)
			}
			if ref.Integration == "opencode-go" || errors.Is(err, ErrUnsupported) {
				windows, err = m.readOpenCodeKeyUsage(ctx, value.Key)
			}
		default:
			return empty, ErrUnsupported
		}
		if err != nil {
			return empty, err
		}
		if len(windows) == 0 {
			return empty, errOpenCodeUsage
		}
		return store.Observation{ObservedAt: time.Now().UTC(), Windows: windows}, nil
	}
	return empty, ErrNotSignedIn
}
