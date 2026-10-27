package realtime

// Ported from qwen-audio-agent server/src/voice/providers/registry.mjs and
// provider-registry.mjs.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// Provider is one realtime voice service.
type Provider struct {
	Key     string
	Label   string
	Aliases []string
	// InputRate and OutputRate are the sample rates, in hertz, of the PCM16
	// mono audio the service takes and sends.
	InputRate, OutputRate int
	Caps                  Capabilities
	Protocol              Protocol
	// Endpoint gives the WebSocket URL and headers for one connection.
	Endpoint func(ctx context.Context) (url string, header http.Header, err error)
	// Session builds the service's session object from Dyna's session.
	Session func(Session) any
	// ContentRefused reports whether a provider error with code and message
	// is a content-safety refusal of the conversation. Nil means the
	// provider has none.
	ContentRefused func(code, message string) bool
	// ResponseStartTimeout is how long a response asked for may take to
	// start; zero means DefaultResponseStart.
	ResponseStartTimeout time.Duration
}

// Capabilities are the differences between providers that the call must
// work around.
type Capabilities struct {
	// FunctionCalling: the model can call tools, so Voice can reach root.
	FunctionCalling bool
	// SingleResponseSlot: the service refuses a response.create while a
	// response is active, instead of queueing it. Agent sessions wait for
	// every provider, so they do not read this flag.
	SingleResponseSlot bool
	// AcknowledgesSessionUpdate: the service answers session.update with
	// session.updated, so Dial can wait for it.
	AcknowledgesSessionUpdate bool
	// EndsStreamOnMute: the service never says when the owner starts to
	// speak, so the call ends the owner's audio stream on every mute that
	// follows audio, to close the turn (Google Live's audioStreamEnd).
	EndsStreamOnMute bool
	// QueuesBeforeSession: the server queues a new client and says when it
	// may send its session (MiniCPM-o's session.queue_done).
	QueuesBeforeSession bool
	// NoClientResponses: the client cannot add items or ask for replies, so
	// Dyna's items cannot be said and are reported unspoken at once.
	NoClientResponses bool
	// ImageInput: the service takes an image as a conversation item, and its
	// Protocol is an ImageProtocol.
	ImageInput bool
	// SteppedByInput: the model moves one step per input chunk, so while
	// the owner is muted the call keeps sending a second of silence each
	// second, and the model can finish its reply (MiniCPM-o full duplex).
	SteppedByInput bool
}

// Session is what Dyna wants from every provider's session.
type Session struct {
	Instructions string
	Tools        []Tool
	Voice        string
	// TranscriptionPrompt lists the words the owner's transcription should expect.
	TranscriptionPrompt string
	Language            string
}

// Registry finds a provider by its key or an alias.
type Registry struct {
	byName map[string]Provider
}

// NewRegistry registers providers. A name used twice is a programming error.
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{byName: map[string]Provider{}}
	for _, p := range providers {
		for _, name := range append([]string{p.Key}, p.Aliases...) {
			name = strings.ToLower(name)
			if _, dup := r.byName[name]; dup {
				panic("realtime: provider name registered twice: " + name)
			}
			r.byName[name] = p
		}
	}
	return r
}

// Lookup returns the provider with that key or alias, in any case.
func (r *Registry) Lookup(name string) (Provider, error) {
	if p, ok := r.byName[strings.ToLower(strings.TrimSpace(name))]; ok {
		return p, nil
	}
	return Provider{}, fmt.Errorf("unknown voice provider %q; known: %s", name, strings.Join(r.Keys(), ", "))
}

// Keys lists each registered provider's key once, sorted.
func (r *Registry) Keys() []string {
	var keys []string
	for _, p := range r.byName {
		if !slices.Contains(keys, p.Key) {
			keys = append(keys, p.Key)
		}
	}
	sort.Strings(keys)
	return keys
}
