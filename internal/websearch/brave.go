package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// BraveEndpoint is the Brave Search API.
const BraveEndpoint = "https://api.search.brave.com"

// braveClient is how long one Brave request may take.
var braveClient = &http.Client{Timeout: 20 * time.Second}

// Brave searches the Brave Search API with the owner's saved API key.
type Brave struct {
	// BaseURL is the API root; empty uses BraveEndpoint.
	BaseURL string
	// Key reads the owner's Brave API key, or returns ErrNotSignedIn when the
	// owner saved none.
	Key func(ctx context.Context) (string, error)
	// Client sends the request; nil uses a client with a 20 s timeout.
	Client *http.Client
}

// Name is "brave".
func (b Brave) Name() string { return "brave" }

// Search asks Brave for q.Results.
func (b Brave) Search(ctx context.Context, q Query) ([]Result, error) {
	if b.Key == nil {
		return nil, ErrNotSignedIn
	}
	key, err := b.Key(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, ErrNotSignedIn
	}
	base := b.BaseURL
	if base == "" {
		base = BraveEndpoint
	}
	endpoint, err := url.Parse(strings.TrimSuffix(base, "/") + "/res/v1/web/search")
	if err != nil {
		return nil, fmt.Errorf("brave endpoint is not a URL: %w", err)
	}
	pairs := endpoint.Query()
	pairs.Set("q", q.Text)
	pairs.Set("count", strconv.Itoa(q.results()))
	endpoint.RawQuery = pairs.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Subscription-Token", key)
	request.Header.Set("Accept", "application/json")
	client := b.Client
	if client == nil {
		client = braveClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("brave request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return nil, statusError("brave", response.StatusCode, response.Body, key)
	}
	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&body); err != nil {
		return nil, errors.New("brave answered a response Dyna cannot read")
	}
	results := make([]Result, 0, len(body.Web.Results))
	for _, found := range body.Web.Results {
		if found.URL == "" {
			continue
		}
		results = append(results, Result{Title: text(found.Title), URL: found.URL, Snippet: text(found.Description)})
	}
	return capped(b.Name(), q, results), nil
}
