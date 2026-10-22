package native

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// claudeUsageURL is the OAuth usage endpoint that Claude usage collectors read
// with the same headers.
const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

// ClaudeUsageOptions lets tests replace the network. Zero values use the real endpoint.
type ClaudeUsageOptions struct {
	Client *http.Client
	URL    string
	Now    func() time.Time
}

// ClaudeUsage is the raw usage response for the account that was active both
// before and after the request. Body holds quota data only, never credentials.
type ClaudeUsage struct {
	Identity   string
	Body       []byte
	ObservedAt time.Time
}

// FetchClaudeUsage sends one read-only GET with the active native access token.
// It never refreshes: a missing or expiring token returns ErrInvalidGrant, and
// the caller reports quota as unknown. A change of the active account during
// the request discards the response.
func FetchClaudeUsage(ctx context.Context, h Host, o ClaudeUsageOptions) (ClaudeUsage, error) {
	m := &claudeManager{host: h}
	before, err := m.Capture(ctx, "")
	if err != nil {
		return ClaudeUsage{}, err
	}
	if len(before) != 1 {
		return ClaudeUsage{}, ErrNoIdentity
	}
	_, tok, identity, _, err := claudeDecode(before[0].Credential)
	if err != nil {
		return ClaudeUsage{}, err
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if tok.AccessToken == "" || !time.UnixMilli(tok.ExpiresAt).After(now().Add(time.Minute)) {
		return ClaudeUsage{}, ErrInvalidGrant
	}
	endpoint := o.URL
	if endpoint == "" {
		endpoint = claudeUsageURL
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ClaudeUsage{}, errors.New("Claude usage could not be requested.")
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", claudeUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return ClaudeUsage{}, errors.New("Claude usage could not be reached.")
	}
	defer resp.Body.Close()
	if err := directStatusError(resp, now()); err != nil {
		return ClaudeUsage{}, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		return ClaudeUsage{}, errors.New("Claude usage response could not be read.")
	}
	if resp.StatusCode != http.StatusOK {
		return ClaudeUsage{}, errors.New("Claude usage returned status " + strconv.Itoa(resp.StatusCode) + ".")
	}
	after, err := m.Capture(ctx, "")
	if err != nil || len(after) != 1 || after[0].Identity != identity {
		return ClaudeUsage{}, errors.New("The Claude account changed during the usage check.")
	}
	return ClaudeUsage{Identity: identity, Body: body, ObservedAt: now().UTC()}, nil
}
