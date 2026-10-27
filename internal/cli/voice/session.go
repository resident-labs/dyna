package voice

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/voice/agentwire"
)

// sessionConn is the part of the engine socket a session uses.
type sessionConn interface {
	ReadMessage() (messageType int, data []byte, err error)
	WriteJSON(v any) error
	Close() error
}

func newSessionCommand() *cobra.Command {
	var req agentwire.SessionRequest
	var promptFile, toolsFile, turns string
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Run a live two-way voice session",
		Long: "Run a live voice conversation that your own program drives, for a voice character, a voice front end\n" +
			"for an app, a kiosk or a phone prototype, or to test a voice app.\n\n" +
			"Write JSON lines to stdin and read JSON lines from stdout. Audio is base64 PCM16 mono at --rate.\n" +
			"Dyna holds the provider connection, so your program never sees the account token. One agent session\n" +
			"runs at a time. It ends at --max-minutes, after 60 seconds with no input audio, or when stdin ends.\n" +
			"The voice provider hears every voice you send, so tell the people who talk to your app.\n\n" +
			"Lines in:  {\"type\":\"audio\",\"pcm\":\"...\"}  {\"type\":\"text\",\"text\":\"...\"} (context; \"respond\":true asks for one reply)\n" +
			"           {\"type\":\"say\",\"text\":\"...\"}  {\"type\":\"tool_result\",\"call_id\":\"...\",\"output\":\"...\"}\n" +
			"           {\"type\":\"interrupt\"}  {\"type\":\"commit\"} (end the turn now; the only end when --turns is manual)  {\"type\":\"image\",\"path\":\"/abs/file.png\"} (PNG or JPEG, up to 5 MB)\n" +
			"Lines out: ready, audio, speech_started, transcript, tool_call, response_done, error, closed.\n" +
			"See docs/voice.md, \"Live sessions for agents\".",
		Example: "  dyna voice session --prompt-file persona.md --voice marin --tools tools.json --rate 48000 < mic.jsonl > replies.jsonl",
		Args:    cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if promptFile != "" {
				b, err := os.ReadFile(promptFile)
				if err != nil {
					return err
				}
				req.Instructions = string(b)
			}
			if toolsFile != "" {
				tools, err := readTools(toolsFile)
				if err != nil {
					return err
				}
				req.Tools = tools
			}
			req.Turns = agentwire.TurnMode(turns)
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt)
			defer stop()
			conn, err := engineclient.New("").Stream(ctx, "/api/voice/session")
			if err != nil {
				return fmt.Errorf("the session did not start: %w", err)
			}
			return runSession(ctx, conn, req, c.InOrStdin(), c.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&promptFile, "prompt-file", "", "file with the instructions for the voice (default: a short friendly assistant)")
	cmd.Flags().StringVar(&req.Voice, "voice", "", "voice name; the provider's presets only (default: the provider's default voice)")
	cmd.Flags().StringVar(&req.Provider, "provider", "", "voice provider (default: the provider in Settings > Voice)")
	cmd.Flags().StringVar(&toolsFile, "tools", "", "JSON file with a list of {name, description, parameters, after} tools; after is reply (default), later or silent")
	cmd.Flags().IntVar(&req.Rate, "rate", agentwire.DefaultSessionRate, "sample rate in hertz of the audio you send and read")
	cmd.Flags().IntVar(&req.MaxMinutes, "max-minutes", agentwire.DefaultSessionMinutes, fmt.Sprintf("longest session, at most %d", agentwire.MaxSessionMinutes))
	cmd.Flags().StringVar(&req.VADEagerness, "vad-eagerness", "", "how soon turn detection ends a turn: low, medium, high or auto (default: the provider's own)")
	cmd.Flags().StringVar(&turns, "turns", string(agentwire.TurnsAuto), "who ends a turn: auto (the provider) or manual (only a commit line)")
	return cmd
}

// readTools reads the tools file: a JSON list of tools.
func readTools(path string) ([]agentwire.SessionTool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var tools []agentwire.SessionTool
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tools); err != nil {
		return nil, fmt.Errorf("%s must be a JSON list of {name, description, parameters, after}: %w", path, err)
	}
	return tools, nil
}

// lockedWriter lets the input and output loops share stdout.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) line(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := l.w.Write(append(b, '\n'))
	return err
}

// stdinLine is a line from the program: a session line, plus the path an
// image line names.
type stdinLine struct {
	agentwire.InMessage
	Path string `json:"path,omitempty"`
}

// readImage loads the file an image line names.
func readImage(path string) (data []byte, mime string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if info.Size() > agentwire.MaxImageBytes {
		return nil, "", fmt.Errorf("%s is %d bytes; an image takes at most %d MB", path, info.Size(), agentwire.MaxImageBytes>>20)
	}
	if data, err = os.ReadFile(path); err != nil {
		return nil, "", err
	}
	switch mime = http.DetectContentType(data); mime {
	case "image/png", "image/jpeg":
		return data, mime, nil
	}
	return nil, "", fmt.Errorf("%s is %s; an image must be PNG or JPEG", path, mime)
}

// runSession starts a session on conn, copies stdin to it and its lines to
// stdout, and returns when the session ends. It returns an error when the
// session never started.
func runSession(ctx context.Context, conn sessionConn, req agentwire.SessionRequest, stdin io.Reader, stdout io.Writer) error {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	out := &lockedWriter{w: stdout}
	if err := conn.WriteJSON(agentwire.InMessage{Type: agentwire.InStart, Request: &req}); err != nil {
		return err
	}
	go pumpInput(conn, stdin, out)
	ready := false
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("the engine ended the session without saying why")
		}
		var m agentwire.OutMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("the engine sent a line this command cannot read: %w", err)
		}
		switch {
		case m.Type == agentwire.OutError && !ready:
			// The session never started: this is the command's failure.
			return errors.New(m.Message)
		case m.Type == agentwire.OutReady:
			ready = true
		}
		if err := out.line([]byte(strings.TrimSpace(string(data)))); err != nil {
			return err
		}
		if m.Type == agentwire.OutClosed {
			return nil
		}
	}
}

// pumpInput sends each stdin line to the engine, and an end line at EOF.
func pumpInput(conn sessionConn, stdin io.Reader, out *lockedWriter) {
	in := bufio.NewScanner(stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	fail := func(format string, args ...any) {
		b, _ := json.Marshal(agentwire.OutMessage{Type: agentwire.OutError, Message: fmt.Sprintf(format, args...)})
		_ = out.line(b)
	}
	for in.Scan() {
		raw := strings.TrimSpace(in.Text())
		if raw == "" {
			continue
		}
		var line stdinLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			fail("a stdin line is not JSON: %v", err)
			continue
		}
		m := line.InMessage
		if m.Type == agentwire.InImage {
			var err error
			if m.Image, m.Mime, err = readImage(line.Path); err != nil {
				fail("%v", err)
				continue
			}
		}
		if err := conn.WriteJSON(m); err != nil {
			return
		}
	}
	if err := in.Err(); err != nil {
		fail("stdin could not be read: %v", err)
	}
	_ = conn.WriteJSON(agentwire.InMessage{Type: agentwire.InEnd})
}
