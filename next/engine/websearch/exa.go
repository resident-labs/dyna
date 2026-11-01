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

// ExaEndpoint is the Exa search API.
const ExaEndpoint = "https://api.exa.ai"

// exaClient is how long one Exa request may take.
var exaClient = &http.Client{Timeout: 20 * time.Second}

// Exa searches the Exa API with the owner's saved API key.
type Exa struct {
	// BaseURL is the API root; empty uses ExaEndpoint.
	BaseURL string
	// Key reads the owner's Exa API key, or returns ErrNotSignedIn when the
	// owner saved none.
	Key func(ctx context.Context) (string, error)
	// Client sends the request; nil uses a client with a 20 s timeout.
	Client *http.Client
}

// Name is "exa".
func (e Exa) Name() string { return "exa" }

// Search asks Exa for q.Results. Highlights hold the snippet.
func (e Exa) Search(ctx context.Context, q Query) ([]Result, error) {
	if e.Key == nil {
		return nil, ErrNotSignedIn
	}
	key, err := e.Key(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, ErrNotSignedIn
	}
	body, err := json.Marshal(exaRequest{
		Query:      q.Text,
		NumResults: q.results(),
		Contents:   exaContents{Highlights: true},
	})
	if err != nil {
		return nil, err
	}
	base := e.BaseURL
	if base == "" {
		base = ExaEndpoint
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("x-api-key", key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := e.Client
	if client == nil {
		client = exaClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("exa request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError("exa", response.StatusCode, response.Body, key)
	}
	var answer struct {
		Results []struct {
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Highlights []string `json:"highlights"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&answer); err != nil {
		return nil, errors.New("exa answered a response Dyna cannot read")
	}
	results := make([]Result, 0, len(answer.Results))
	for _, found := range answer.Results {
		if found.URL == "" {
			continue
		}
		results = append(results, Result{
			Title:   text(found.Title),
			URL:     found.URL,
			Snippet: text(strings.Join(found.Highlights, " ")),
		})
	}
	return capped(e.Name(), q, results), nil
}

// exaRequest is the body of a search request.
type exaRequest struct {
	Query      string      `json:"query"`
	NumResults int         `json:"numResults"`
	Contents   exaContents `json:"contents"`
}

// exaContents asks for the highlights that become each result's snippet.
type exaContents struct {
	Highlights bool `json:"highlights"`
}
