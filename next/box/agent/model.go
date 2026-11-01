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

// socketWait is how long a request waits for the box socket. It covers an engine restart on any host, and is short enough
// that a host gone for good still ends in a 502 and Pi's own handling. It is a variable so that tests can shorten it.
var socketWait = 5 * time.Minute

// ModelRoute forwards the incoming HTTP request over the box socket. Only Pi retries a request that reached the engine, but
// one that could not dial reached nothing, so it waits here for the socket. An engine restart takes the socket away for a
// while, and answering 502 at once would use up Pi's retries and fail a turn the restart should not touch.
// ReverseProxy streams responses immediately, including server-sent events.
func ModelRoute(socket string) http.Handler {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialWaiting(ctx, socket)
		},
		DisableKeepAlives: true,
	}
	return modelRoute(transport)
}

func dialWaiting(ctx context.Context, socket string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, socketWait)
	defer cancel()
	delay := 100 * time.Millisecond
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err == nil {
			return conn, nil
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, err
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
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
