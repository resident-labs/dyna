// Package agent contains the box-side services.
package agent

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// socketWait is how long a request waits for the engine. It covers an engine restart on any host, and is short enough that a
// host gone for good still ends in a 502 and Pi's own handling. It is a variable so that tests can shorten it.
var socketWait = 5 * time.Minute

// ModelRoute forwards the incoming HTTP request over the box socket. A request that reached no engine waits here and goes again,
// with the call queue's backoff. An engine restart takes the socket away for a while, and answering 502 at once would use up
// Pi's retries and fail a turn the restart should not touch. ReverseProxy streams responses immediately, including server-sent
// events.
func ModelRoute(socket string) http.Handler {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialWaiting(ctx, socket)
		},
		DisableKeepAlives: true,
	}
	return modelRoute(holding{transport})
}

// dialWaiting waits for the socket. A request that could not dial sent nothing, whatever its route.
func dialWaiting(ctx context.Context, socket string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, socketWait)
	defer cancel()
	var delay backoff
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err == nil || !delay.wait(ctx) {
			return conn, err
		}
	}
}

// holding sends a model call again when its connection closes before any response headers. The engine's drain holds new calls
// without sending them on, and closes the socket on them as it exits, so such a call reached no provider. A model call is kept in
// memory for this; git's requests stream, so only a failed dial sends one again. A call that fails after its headers stays failed.
type holding struct{ next http.RoundTripper }

func (h holding) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, "/model/") {
		return h.next.RoundTrip(req)
	}
	var kept []byte
	if req.Body != nil {
		var err error
		kept, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	waiting, cancel := context.WithTimeout(req.Context(), socketWait)
	defer cancel()
	var delay backoff
	for {
		attempt := req.Clone(req.Context())
		attempt.Body, attempt.ContentLength = http.NoBody, int64(len(kept))
		if len(kept) > 0 {
			attempt.Body = io.NopCloser(bytes.NewReader(kept))
		}
		resp, err := h.next.RoundTrip(attempt)
		if err == nil || !delay.wait(waiting) {
			return resp, err
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
			if !strings.HasPrefix(r.URL.Path, "/git/") && !strings.HasPrefix(r.URL.Path, "/github/") {
				r.URL.Path = "/model" + r.URL.Path
				if r.URL.RawPath != "" {
					r.URL.RawPath = "/model" + r.URL.RawPath
				}
			}
		},
	}
}

// BuildCacheRoute passes sccache's requests to the host's compiler cache, whose socket the box mounts. It never waits for the
// socket: a request that fails is a cache miss, and the build compiles the crate itself. sccache counts those, so they are not
// logged, which while the cache is down would be two lines for every crate.
func BuildCacheRoute(socket string) http.Handler {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &httputil.ReverseProxy{
		Transport:    transport,
		Rewrite:      func(r *httputil.ProxyRequest) { r.Out.URL.Scheme, r.Out.URL.Host = "http", "build-cache" },
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) },
	}
}

// ModelProvider describes a generated Pi provider and its API origin.
type ModelProvider struct {
	BaseURL string            `json:"baseUrl"`
	APIKey  string            `json:"apiKey"`
	Headers map[string]string `json:"headers,omitempty"`
	Models  []ModelDefinition `json:"models,omitempty"`
}

type ModelDefinition struct {
	ID            string   `json:"id"`
	API           string   `json:"api"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
}

type ModelsConfig struct {
	Providers map[string]ModelProvider `json:"providers"`
}

func Models(profiles ...contract.ProfileSummary) ModelsConfig {
	origin := "http://127.0.0.1:7781"
	config := ModelsConfig{Providers: map[string]ModelProvider{
		"anthropic": {BaseURL: origin, APIKey: "box"},
		"openai":    {BaseURL: origin + "/v1", APIKey: "box"},
	}}
	for _, profile := range profiles {
		if profile.Images == nil || *profile.Images {
			continue
		}
		provider := config.Providers[string(profile.Provider)]
		// Pi's fallback uses the provider's default model. These limits and APIs are from
		// the installed Pi catalog's gpt-5.5 and claude-opus-4-8, respectively.
		model := ModelDefinition{ID: profile.Model, Reasoning: profile.Thinking != contract.ThinkingOff, Input: []string{"text"}, MaxTokens: 128000}
		switch profile.Provider {
		case contract.ModelProviderOpenAI:
			model.API, model.ContextWindow = "openai-responses", 272000
		case contract.ModelProviderAnthropic:
			model.API, model.ContextWindow = "anthropic-messages", 1000000
		}
		provider.Models = append(provider.Models, model)
		config.Providers[string(profile.Provider)] = provider
	}
	return config
}

// WorkerModels is one crew worker's configuration. The engine's route reads the header, so each worker holds its own account
// affinity although a crew is one session.
func WorkerModels(worker string, profiles ...contract.ProfileSummary) ModelsConfig {
	config := Models(profiles...)
	for name, provider := range config.Providers {
		provider.Headers = map[string]string{contract.WorkerHeader: worker}
		config.Providers[name] = provider
	}
	return config
}
