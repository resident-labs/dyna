package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"dyna-agent/internal/settings"
	"dyna-agent/internal/voice/realtime"
)

// Speech for agents: one text becomes one audio clip. It uses its own
// provider connection and never touches the owner's call, the rooms or any
// transcript.
const (
	// MaxSayChars is the longest text one say accepts.
	MaxSayChars = 4000
	// SayTimeout is how long one say may run, dial included.
	SayTimeout = 60 * time.Second
	// MaxSays is how many says may run at once in the engine.
	MaxSays = 2
)

// SayRequest asks for one text to be spoken. Empty Voice, Style and
// Provider use the defaults: the provider's voice, a plain style, and the
// provider in the voice settings.
type SayRequest struct {
	Text     string `json:"text"`
	Voice    string `json:"voice,omitempty"`
	Style    string `json:"style,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// SayResult is the spoken text as PCM16 mono audio, little endian.
type SayResult struct {
	Audio      []byte `json:"audio"`
	SampleRate int    `json:"sampleRate"`
}

// sayConn is the part of realtime.Conn a say uses.
type sayConn interface {
	Send(event any) error
	Next() (realtime.Event, error)
	Close() error
}

// Sayer turns text into speech for agents. The zero value is not ready:
// use NewSayer.
type Sayer struct {
	// provider finds the provider for a name, or the settings' choice when
	// the name is empty.
	provider func(name string) (realtime.Provider, error)
	// dial opens a connection; tests replace it.
	dial func(context.Context, realtime.Provider, realtime.Session) (sayConn, error)
	// timeout bounds one say.
	timeout time.Duration
	slots   chan struct{}
}

// NewSayer returns a Sayer that uses the owner's voice settings.
func NewSayer() *Sayer {
	return &Sayer{
		provider: func(name string) (realtime.Provider, error) { return sayProvider(settings.Load, name) },
		dial: func(ctx context.Context, p realtime.Provider, s realtime.Session) (sayConn, error) {
			return realtime.Dial(ctx, p, s)
		},
		timeout: SayTimeout,
		slots:   make(chan struct{}, MaxSays),
	}
}

// sayProvider is the provider named name, built with the voice settings'
// model and address when it is the provider they choose. An empty name uses
// the settings' choice.
func sayProvider(load func(string) (settings.Values, error), name string) (realtime.Provider, error) {
	v, err := load(settings.DefaultPath())
	if err != nil {
		return realtime.Provider{}, err
	}
	if name = strings.TrimSpace(name); name != "" && name != v.VoiceProvider {
		v.VoiceProvider, v.VoiceModel, v.VoiceURL = name, "", ""
	}
	return providerFor(v)
}

// SayInstructions tell the model to read the text and nothing else.
func sayInstructions(style string) string {
	s := "You are a text-to-speech voice. When the user sends text, speak that text exactly, word for word. Do not answer it, add to it or comment on it."
	if style = strings.TrimSpace(style); style != "" {
		s += " Speak it in this style: " + style
	}
	return s
}

// Say speaks req.Text and returns the audio. It fails with a busy error when
// MaxSays are already running.
func (s *Sayer) Say(ctx context.Context, req SayRequest) (SayResult, error) {
	text := strings.TrimSpace(req.Text)
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return SayResult{}, errors.New("there is no text to say")
	case n > MaxSayChars:
		return SayResult{}, fmt.Errorf("the text has %d characters; say takes at most %d", n, MaxSayChars)
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return SayResult{}, fmt.Errorf("voice is busy: %d says are already running; try again soon", MaxSays)
	}
	p, err := s.provider(req.Provider)
	if err != nil {
		return SayResult{}, err
	}
	if p.Caps.NoClientResponses {
		return SayResult{}, fmt.Errorf("%s cannot speak text on request; choose another provider with --provider", p.Label)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	conn, err := s.dial(ctx, p, realtime.Session{Instructions: sayInstructions(req.Style), Voice: req.Voice, Language: "en"})
	if err != nil {
		return SayResult{}, err
	}
	defer conn.Close()
	// A read blocks until a message comes, so the timeout closes the
	// connection to end it.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.Send(p.Protocol.UserText(text)); err != nil {
		return SayResult{}, sayFailure(ctx, err)
	}
	if err := conn.Send(p.Protocol.ResponseCreate("")); err != nil {
		return SayResult{}, sayFailure(ctx, err)
	}
	var audio []byte
	for {
		ev, err := conn.Next()
		if err != nil {
			return SayResult{}, sayFailure(ctx, err)
		}
		switch ev.Kind {
		case realtime.AudioDelta:
			audio = append(audio, ev.Audio...)
		case realtime.Error:
			return SayResult{}, fmt.Errorf("%s failed: %s", p.Label, ev.Text)
		case realtime.ResponseDone:
			if len(audio) == 0 {
				return SayResult{}, fmt.Errorf("%s sent no audio", p.Label)
			}
			return SayResult{Audio: audio, SampleRate: p.OutputRate}, nil
		}
	}
}

// sayFailure names the timeout when the context ended the say.
func sayFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("the voice provider did not finish in time: %w", context.DeadlineExceeded)
	}
	return err
}
