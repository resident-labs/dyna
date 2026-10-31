// Package agent contains the box-side services.
package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
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

func Models(routes map[contract.ModelProvider]string) ModelsConfig {
	providers := make(map[string]ModelProvider, len(routes))
	for provider, suffix := range routes {
		providers[string(provider)] = ModelProvider{BaseURL: "http://127.0.0.1:7781" + suffix, APIKey: "box"}
	}
	return ModelsConfig{Providers: providers}
}
