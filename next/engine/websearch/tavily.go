package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TavilyEndpoint is the Tavily search API.
const TavilyEndpoint = "https://api.tavily.com"

// tavilyClient is how long one Tavily request may take.
var tavilyClient = &http.Client{Timeout: 20 * time.Second}

// Tavily searches the Tavily API with the owner's saved API key.
type Tavily struct {
	// BaseURL is the API root; empty uses TavilyEndpoint.
	BaseURL string
	// Key reads the owner's Tavily API key, or returns ErrNotSignedIn when the
	// owner saved none.
	Key func(ctx context.Context) (string, error)
	// Client sends the request; nil uses a client with a 20 s timeout.
	Client *http.Client
}

// Name is "tavily".
func (t Tavily) Name() string { return "tavily" }

// Search asks Tavily for q.Results.
func (t Tavily) Search(ctx context.Context, q Query) ([]Result, error) {
	if t.Key == nil {
		return nil, ErrNotSignedIn
	}
	key, err := t.Key(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, ErrNotSignedIn
	}
	body, err := json.Marshal(tavilyRequest{Query: q.Text, MaxResults: q.results()})
	if err != nil {
		return nil, err
	}
	base := t.BaseURL
	if base == "" {
		base = TavilyEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := t.Client
	if client == nil {
		client = tavilyClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("tavily request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError("tavily", response.StatusCode, response.Body, key)
	}
	var answer struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&answer); err != nil {
		return nil, errors.New("tavily answered a response Dyna cannot read")
	}
	results := make([]Result, 0, len(answer.Results))
	for _, found := range answer.Results {
		if found.URL == "" {
			continue
		}
		results = append(results, Result{Title: text(found.Title), URL: found.URL, Snippet: text(found.Content)})
	}
	return capped(t.Name(), q, results), nil
}

// tavilyRequest is the body of a search request.
type tavilyRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}
