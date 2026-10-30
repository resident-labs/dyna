// Package agent contains the box-side services.
package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"
)

// ModelRoute forwards the incoming HTTP request over the box socket without retry.
// ReverseProxy streams responses immediately, including server-sent events.
func ModelRoute(socket string) http.Handler {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	return modelRoute(transport)
}

func modelRoute(transport http.RoundTripper) http.Handler {
	return &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -time.Nanosecond,
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.URL.Host = "dyna"
			if !strings.HasPrefix(r.URL.Path, "/git/") {
				r.URL.Path = "/model" + r.URL.Path
				if r.URL.RawPath != "" {
					r.URL.RawPath = "/model" + r.URL.RawPath
				}
			}
		},
	}
}

// ModelProvider describes a generated Pi provider and its API origin.
type ModelProvider struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
}

type ModelsConfig struct {
	Providers map[string]ModelProvider `json:"providers"`
}

func Models() ModelsConfig {
	origin := "http://127.0.0.1:7781"
	return ModelsConfig{Providers: map[string]ModelProvider{
		"anthropic": {BaseURL: origin, APIKey: "box"},
		"openai":    {BaseURL: origin + "/v1", APIKey: "box"},
	}}
}
