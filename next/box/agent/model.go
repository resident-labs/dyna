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
var socketWait = 120 * time.Second

// ModelRoute forwards the incoming HTTP request over the box socket. A request that reached no engine waits here and goes again,
// with the call queue's backoff. An engine restart takes the socket away for a while, and answering 502 at once would use up
// Pi's retries and fail a turn the restart should not touch. ReverseProxy streams responses immediately, including server-sent
// events.
func ModelRoute(socket string) http.Handler {
	// net/http may finish a canceled dial after RoundTrip returns. Capture the
	// budget at construction so that detached dial never reads a mutable seam.
	wait := socketWait
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialWaiting(ctx, socket, wait)
		},
		DisableKeepAlives: true,
	}
	return modelRoute(holding{next: transport, wait: wait})
}

// dialWaiting waits for the socket. A request that could not dial sent nothing, whatever its route.
func dialWaiting(ctx context.Context, socket string, wait time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
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
type holding struct {
	next http.RoundTripper
	wait time.Duration
}

func (h holding) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, "/model/") {
		return h.next.RoundTrip(req)
	}
	var kept []byte
	if req.Body != nil {
		var err error
		// ContentLength is only an allocation hint, never a read limit. Leave
		// EOF-read slack so ReadFrom does not double a full buffer just for EOF.
		const maxPreallocation = 16 << 20
		if req.ContentLength > 0 && req.ContentLength <= maxPreallocation-512 {
			var buffer bytes.Buffer
			buffer.Grow(int(req.ContentLength) + 512)
			_, err = buffer.ReadFrom(req.Body)
			kept = buffer.Bytes()
		} else {
			kept, err = io.ReadAll(req.Body)
		}
		req.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	waiting, cancel := context.WithTimeout(req.Context(), h.wait)
	defer cancel()
	var delay backoff
	for {
		attemptCtx, cancelAttempt := context.WithCancel(req.Context())
		stopBudget := context.AfterFunc(waiting, cancelAttempt)
		attempt := req.Clone(attemptCtx)
		attempt.Body, attempt.ContentLength = http.NoBody, int64(len(kept))
		if len(kept) > 0 {
			attempt.Body = io.NopCloser(bytes.NewReader(kept))
		}
		resp, err := h.next.RoundTrip(attempt)
		stopBudget()
		if err == nil {
			// Headers end the restart hold. A healthy response may stream for hours.
			resp.Body = &cancelModelBody{ReadCloser: resp.Body, cancel: cancelAttempt}
			return resp, nil
		}
		cancelAttempt()
		if !delay.wait(waiting) {
			return resp, err
		}
	}
}

type cancelModelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelModelBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}

func modelRoute(transport http.RoundTripper) http.Handler {
	return &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -time.Nanosecond,
		//lint:ignore SA1019 Keep Director's existing forwarding-header semantics until a separately tested proxy migration.
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
	ID               string             `json:"id"`
	API              string             `json:"api"`
	Reasoning        bool               `json:"reasoning"`
	Input            []string           `json:"input"`
	ContextWindow    int                `json:"contextWindow"`
	MaxTokens        int                `json:"maxTokens"`
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap,omitempty"`
	Compat           map[string]bool    `json:"compat,omitempty"`
	PromptCache      map[string]int     `json:"promptCache,omitempty"`
	InputLimits      *modelInputLimits  `json:"inputLimits,omitempty"`
}

type modelInputLimits struct {
	MaxRequestBytes int              `json:"maxRequestBytes"`
	Images          modelImageLimits `json:"images"`
}
type modelImageLimits struct {
	MaxPerRequest int              `json:"maxPerRequest"`
	Resize        modelImageResize `json:"resize"`
}
type modelImageResize struct {
	MaxWidth    int `json:"maxWidth"`
	MaxHeight   int `json:"maxHeight"`
	MaxBytes    int `json:"maxBytes"`
	JPEGQuality int `json:"jpegQuality"`
}

type ModelsConfig struct {
	Providers map[string]ModelProvider `json:"providers"`
}

func Models(textOnly ...contract.TextOnlyModel) ModelsConfig {
	origin := "http://127.0.0.1:7781"
	config := ModelsConfig{Providers: map[string]ModelProvider{
		"anthropic": {BaseURL: origin, APIKey: "box"},
		"openai":    {BaseURL: origin + "/v1", APIKey: "box"},
	}}
	for _, entry := range textOnly {
		provider := config.Providers[string(entry.Provider)]
		model := ModelDefinition{ID: entry.Model, Reasoning: true, Input: []string{"text"}, MaxTokens: 128000}
		if entry.Images {
			model.Input = append(model.Input, "image")
		}
		model.InputLimits = &modelInputLimits{Images: modelImageLimits{Resize: modelImageResize{MaxWidth: 2000, MaxHeight: 2000, MaxBytes: 4718592, JPEGQuality: 80}}}
		level := func(s string) *string { return &s }
		// Pi ai 1.4.0-dyna.13.228dd4febd41, catalog openai/gpt-5.5.
		switch entry.Provider {
		case contract.ModelProviderOpenAI:
			model.API, model.ContextWindow = "openai-responses", 272000
			model.Compat = map[string]bool{"supportsStrictMode": true, "supportsOpenAIGrammarTools": true, "supportsAdditionalTools": true, "supportsToolSearch": true, "supportsMidConvoSystemMessages": true}
			model.ThinkingLevelMap = map[string]*string{"off": level("none"), "minimal": nil, "low": level("low"), "medium": level("medium"), "high": level("high"), "xhigh": level("xhigh"), "max": nil}
			model.InputLimits.MaxRequestBytes, model.InputLimits.Images.MaxPerRequest = 536870912, 1500
		// Pi ai 1.4.0-dyna.13.228dd4febd41, catalog anthropic/claude-opus-4-8.
		case contract.ModelProviderAnthropic:
			model.API, model.ContextWindow = "anthropic-messages", 1000000
			model.Compat = map[string]bool{"supportsMidConvoSystemMessages": true, "supportsMidConvoToolChanges": true, "forceAdaptiveThinking": true, "supportsTemperature": false, "supportsStrictTools": true}
			model.ThinkingLevelMap = map[string]*string{"xhigh": level("xhigh"), "max": level("max")}
			model.PromptCache = map[string]int{"short": 300, "long": 3600}
			model.InputLimits.MaxRequestBytes, model.InputLimits.Images.MaxPerRequest = 33554432, 600
		}
		provider.Models = append(provider.Models, model)
		config.Providers[string(entry.Provider)] = provider
	}
	return config
}

// WorkerModels is one crew worker's configuration. The engine's route reads the header, so each worker holds its own account
// affinity although a crew is one session.
func WorkerModels(worker string, textOnly ...contract.TextOnlyModel) ModelsConfig {
	config := Models(textOnly...)
	for name, provider := range config.Providers {
		provider.Headers = map[string]string{contract.WorkerHeader: worker}
		config.Providers[name] = provider
	}
	return config
}
