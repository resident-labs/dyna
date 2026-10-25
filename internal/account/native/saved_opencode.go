package native

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"dyna-agent/internal/account/store"
)

var errOpenCodeUsage = errors.New("OpenCode returned an unsupported usage response.")

// openCodeUsageRequest sends one read to the OpenCode API with its native
// request identity. It takes no harness credential, so any account of the
// provider can use it. OpenCode 2.0.32 embeds this identity in its binary, and
// Cloudflare rejects the default HTTP client identity with Error 1010.
func openCodeUsageRequest(ctx context.Context, client claudeHTTPClient, now time.Time, path, token, orgID string) ([]byte, error) {
	headers := map[string]string{"Authorization": "Bearer " + token, "Accept": "application/json", "User-Agent": "opencode/latest/2.0.32/cli"}
	if orgID != "" {
		headers["x-org-id"] = orgID
	}
	raw, err := directRequest(ctx, client, http.MethodGet, "https://opencode.ai"+path, "", headers, now)
	var status *UsageHTTPError
	if errors.As(err, &status) && (status.Status == 404 || status.Status == 405) {
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// openCodeKeyUsageObservation parses the Go plan windows of an OpenCode key.
// It takes the provider response only, so any account of the provider can use
// it.
func openCodeKeyUsageObservation(raw []byte, now time.Time) (store.Observation, error) {
	var response struct {
		Usage map[string]struct {
			Percent  *float64 `json:"percent"`
			ResetsAt string   `json:"resetsAt"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return store.Observation{}, errOpenCodeUsage
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
		return store.Observation{}, errOpenCodeUsage
	}
	return store.Observation{ObservedAt: now.UTC(), Windows: windows}, nil
}
