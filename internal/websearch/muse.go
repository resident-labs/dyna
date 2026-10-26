package websearch

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// MuseEndpoint is the Meta Muse API root. Its search path sits next to the
	// model calls under /muse-code.
	MuseEndpoint = "https://api.meta.ai"
	// museSurface is the Muse client surface this engine reports. The gateway
	// expects the surface headers of a Muse client and refuses others.
	museSurface = "tbh:exec"
	// museVersion is the API version header the Muse client sends.
	museVersion = "1.0.0"
	// museAgent is the user agent the Muse client sends.
	museAgent = "muse-build/1.4.12"
	// museMaxBody caps one search response.
	museMaxBody = 4 << 20
)

// museClient is how long one Muse search may take.
var museClient = &http.Client{Timeout: 30 * time.Second}

// Muse searches the Meta Muse search API with the API key of the owner's Muse
// sign-in. The key lives in the sign-in file (~/.config/muse/auth.json, field
// providers.meta.api_key) or in META_API_KEY; the engine reads it and passes
// only the reader, so this provider never touches a file or the environment.
type Muse struct {
	// BaseURL is the API root; empty uses MuseEndpoint.
	BaseURL string
	// Key reads the Muse API key, or returns ErrNotSignedIn when the owner has
	// no Muse sign-in.
	Key func(ctx context.Context) (string, error)
	// Client sends the request; nil uses a client with a 30 s timeout.
	Client *http.Client
}

// Name is "muse".
func (m Muse) Name() string { return "muse" }

// Search asks the Muse search API about q.
func (m Muse) Search(ctx context.Context, q Query) ([]Result, error) {
	if m.Key == nil {
		return nil, ErrNotSignedIn
	}
	key, err := m.Key(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, ErrNotSignedIn
	}
	// One request id names the search in both the body and the session header.
	requestID, err := museRequestID()
	if err != nil {
		return nil, fmt.Errorf("muse request id failed: %w", err)
	}
	base := m.BaseURL
	if base == "" {
		base = MuseEndpoint
	}
	body, err := json.Marshal(museRequest{Query: q.Text, RequestID: requestID})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/muse-code/search", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-Client-Id", museSurface)
	request.Header.Set("X-Tbh-Session-Id", requestID)
	request.Header.Set("X-Api-Version", museVersion)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", museAgent)
	client := m.Client
	if client == nil {
		client = museClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("muse request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError("muse", response.StatusCode, response.Body, key)
	}
	var answer museAnswer
	if err := json.NewDecoder(io.LimitReader(response.Body, museMaxBody)).Decode(&answer); err != nil {
		return nil, errors.New("muse answered a response Dyna cannot read")
	}
	results := make([]Result, 0, len(answer.Results))
	for _, hit := range answer.Results {
		if hit.URL == "" {
			continue
		}
		results = append(results, Result{Title: text(hit.Title), URL: hit.URL, Snippet: text(hit.Snippet)})
	}
	return capped(m.Name(), q, results), nil
}

// museRequest is the body of one search request. The query is the only field
// that changes between two searches.
type museRequest struct {
	Query          string `json:"query"`
	RequestID      string `json:"request_id"`
	IterationIndex int    `json:"iteration_index"`
}

// museAnswer is the part of a search response the provider reads. The answer
// also carries augmented_context, which the provider leaves alone: results is
// the same list in a typed shape.
type museAnswer struct {
	Results []museHit `json:"results"`
}

// museHit is one search result. page_last_modified is a string the provider
// does not need, so it is not declared.
type museHit struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// museRequestID makes a fresh version 4 UUID for one search.
func museRequestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}
