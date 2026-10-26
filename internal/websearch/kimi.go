package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	// KimiEndpoint is the Kimi Code API root. The Kimi Code client posts its
	// search to <root>/search.
	KimiEndpoint = "https://api.kimi.com/coding/v1"
	// KimiVersion is the Kimi Code client version the endpoint expects. The
	// engine may set its own in Kimi.Version.
	KimiVersion = "2.3.2"
	// kimiPlatform is the client platform the Kimi Code API expects.
	kimiPlatform = "kimi_code_cli"
)

// kimiClient is how long one Kimi request may take.
var kimiClient = &http.Client{Timeout: 20 * time.Second}

// Kimi searches the web through the Kimi Code search endpoint, using the
// owner's saved Kimi Code sign-in.
type Kimi struct {
	// BaseURL is the API root; empty uses KimiEndpoint.
	BaseURL string
	// Token reads the Kimi Code OAuth access token, or returns ErrNotSignedIn
	// when the owner signed in to no Kimi Code account.
	Token func(ctx context.Context) (string, error)
	// DeviceID is the device UUID the Kimi Code client sends. Empty omits the
	// header.
	DeviceID string
	// Version is the client version the endpoint expects; empty uses
	// KimiVersion.
	Version string
	// Client sends the request; nil uses a client with a 20 s timeout.
	Client *http.Client
}

// Name is "kimi".
func (k Kimi) Name() string { return "kimi" }

// Search asks the Kimi Code search endpoint about q.
func (k Kimi) Search(ctx context.Context, q Query) ([]Result, error) {
	if k.Token == nil {
		return nil, ErrNotSignedIn
	}
	token, err := k.Token(ctx)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNotSignedIn
	}
	base := k.BaseURL
	if base == "" {
		base = KimiEndpoint
	}
	body, err := json.Marshal(kimiQuery{TextQuery: q.Text})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	k.setHeaders(request, token)
	client := k.Client
	if client == nil {
		client = kimiClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("kimi request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		if response.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("kimi sign-in was refused: %w", ErrNotSignedIn)
		}
		return nil, statusError("kimi", response.StatusCode)
	}
	var answer kimiAnswer
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&answer); err != nil {
		return nil, errors.New("kimi answered a response Dyna cannot read")
	}
	results := make([]Result, 0, len(answer.SearchResults))
	for _, found := range answer.SearchResults {
		if found.URL == "" {
			continue
		}
		results = append(results, Result{Title: text(found.Title), URL: found.URL, Snippet: text(found.Snippet)})
	}
	return capped(k.Name(), q, results), nil
}

// setHeaders sends the token and the client headers the endpoint expects.
func (k Kimi) setHeaders(request *http.Request, token string) {
	version := k.Version
	if version == "" {
		version = KimiVersion
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "kimi-code-cli/"+version)
	request.Header.Set("X-Msh-Platform", kimiPlatform)
	request.Header.Set("X-Msh-Version", version)
	request.Header.Set("X-Msh-Device-Name", kimiDeviceName())
	request.Header.Set("X-Msh-Device-Model", runtime.GOARCH)
	request.Header.Set("X-Msh-Os-Version", runtime.GOOS)
	if k.DeviceID != "" {
		request.Header.Set("X-Msh-Device-Id", k.DeviceID)
	}
}

// kimiDeviceName names this host in the request headers.
func kimiDeviceName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}

// kimiQuery is the body of one search request.
type kimiQuery struct {
	TextQuery string `json:"text_query"`
}

// kimiAnswer is the search response. A missing search_results means no result.
type kimiAnswer struct {
	SearchResults []kimiFound `json:"search_results"`
}

// kimiFound is one result of the answer.
type kimiFound struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}
