package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type managementClient struct {
	client *http.Client
	key    string
}

// request never returns proxy headers, response bodies on errors, or transport errors.
func (m managementClient) request(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	return m.requestAttempt(ctx, method, path, body, "")
}

func (m managementClient) requestAttempt(ctx context.Context, method, path string, body []byte, attempt string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://cliproxy"+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("management request failed")
	}
	if attempt != "" {
		req.Header.Set("X-Dyna-Attempt", attempt)
	}
	req.Header.Set("Authorization", "Bearer "+m.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := m.client.Do(req)
	if err != nil {
		return nil, errors.New("management request failed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnprocessableEntity {
		return nil, ErrModelsUnavailable
	}
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("management request failed")
	}
	if err != nil || !json.Valid(data) {
		return nil, errors.New("management response invalid")
	}
	return data, nil
}
