package model

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
)

// SearchClient routes a search through the proxy using only eligible accounts.
func (p *Proxy) SearchClient(ids []string) *http.Client {
	encoded := make([]string, len(ids))
	for i, id := range ids {
		encoded[i] = base64.RawURLEncoding.EncodeToString([]byte(id))
	}
	return &http.Client{Transport: searchTransport{base: p.client.Transport, key: p.clientKey, eligible: strings.Join(encoded, ",")}, Timeout: 90 * time.Second}
}

type searchTransport struct {
	base     http.RoundTripper
	key      string
	eligible string
}

func (t searchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Host != "cliproxy" || t.eligible == "" {
		return nil, errors.New("search proxy route unavailable")
	}
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	for _, name := range []string{"Authorization", "X-Api-Key", "ChatGPT-Account-ID", "X-Dyna-Eligible-Auth", "Connection"} {
		r.Header.Del(name)
	}
	r.Header.Set("Authorization", "Bearer "+t.key)
	r.Header.Set("X-Api-Key", t.key)
	r.Header.Set("X-Dyna-Eligible-Auth", t.eligible)
	return t.base.RoundTrip(r)
}
