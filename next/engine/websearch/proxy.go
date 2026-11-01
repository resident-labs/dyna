package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// ProxySearch calls the proxy's fixed-upstream search route.
type ProxySearch struct {
	Provider string
	Client   *http.Client
}

func (p ProxySearch) Name() string { return p.Provider }
func (p ProxySearch) Search(ctx context.Context, q Query) ([]Result, error) {
	body, err := json.Marshal(struct {
		Provider string `json:"provider"`
		Query    string `json:"query"`
		Limit    int    `json:"limit"`
	}{p.Provider, q.Text, q.results()})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://cliproxy/dyna/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.Client.Do(request)
	if err != nil {
		return nil, errors.New("search proxy request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError(p.Provider, response.StatusCode, response.Body)
	}
	var out struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&out); err != nil {
		return nil, errors.New("search proxy returned an invalid response")
	}
	return capped(p.Name(), q, out.Results), nil
}
