package visual

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
)

type message struct {
	ID        int                       `json:"id"`
	SessionID string                    `json:"sessionId"`
	Method    string                    `json:"method"`
	Params    json.RawMessage           `json:"params"`
	Result    json.RawMessage           `json:"result"`
	Error     *struct{ Message string } `json:"error"`
}
type client struct {
	input          *os.File
	messages       chan message
	readError      chan error
	stderr         *errorTail
	id             int
	session        string
	doc            []byte
	csp            string
	allowNet       bool
	loaded         bool
	documentServed bool
	blockedNetwork bool
	hosts          map[string]bool
	console        []string
}

func (c *client) read(ctx context.Context, input *os.File) {
	r := bufio.NewReader(input)
	for {
		b, err := r.ReadBytes(0)
		if err != nil {
			c.readError <- c.startError(err)
			return
		}
		var m message
		if err = json.Unmarshal(b[:len(b)-1], &m); err != nil {
			c.readError <- c.startError(err)
			return
		}
		select {
		case c.messages <- m:
		case <-ctx.Done():
			return
		}
	}
}
func (c *client) send(method string, params any) (int, error) {
	c.id++
	b, err := json.Marshal(map[string]any{"id": c.id, "method": method, "params": params, "sessionId": c.session})
	if err != nil {
		return 0, err
	}
	_, err = c.input.Write(append(b, 0))
	return c.id, err
}

// startError adds the last line Chromium wrote to stderr, so a browser that
// exits at start names its own cause instead of only reporting EOF.
func (c *client) startError(err error) error {
	if tail := c.stderr.last(); tail != "" {
		return fmt.Errorf("%w: %s", err, tail)
	}
	return err
}

// errorTail keeps the end of what Chromium wrote to stderr.
type errorTail struct {
	mu   sync.Mutex
	text string
}

// Write keeps the last 8 KiB, so a chatty browser cannot grow the buffer.
func (t *errorTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.text += string(p)
	if len(t.text) > 8<<10 {
		t.text = t.text[len(t.text)-(8<<10):]
	}
	return len(p), nil
}

// last returns the last non-empty line written.
func (t *errorTail) last() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(t.text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

func (c *client) next(ctx context.Context) (message, error) {
	select {
	case m := <-c.messages:
		return m, nil
	case err := <-c.readError:
		return message{}, err
	case <-ctx.Done():
		return message{}, ctx.Err()
	}
}
func (c *client) call(ctx context.Context, method string, params any, out any) error {
	id, err := c.send(method, params)
	if err != nil {
		return err
	}
	for {
		m, err := c.next(ctx)
		if err != nil {
			return err
		}
		if m.ID == id {
			if m.Error != nil {
				return fmt.Errorf("%s: %s", method, m.Error.Message)
			}
			if out != nil {
				return json.Unmarshal(m.Result, out)
			}
			return nil
		}
		if m.Method != "" {
			if err := c.event(m); err != nil {
				return err
			}
		}
	}
}
func (c *client) event(m message) error {
	// A closed tab may still deliver events; they belong to an earlier page.
	if m.SessionID != "" && m.SessionID != c.session {
		return nil
	}
	switch m.Method {
	case "Network.webSocketCreated":
		var p struct{ URL string }
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		c.recordURL(p.URL)
	case "Runtime.bindingCalled":
		var p struct{ Name, Payload string }
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.Name == policyBinding {
			if u := c.recordURL(p.Payload); u != nil && u.Host != "" {
				c.blockedNetwork = true
			}
		}
	case "Network.requestWillBeSent":
		var p struct{ Request struct{ URL string } }
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		c.recordURL(p.Request.URL)
	case "Network.loadingFailed":
		var p struct{ BlockedReason string }
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.BlockedReason != "" {
			c.blockedNetwork = true
		}
	case "Page.loadEventFired":
		c.loaded = true
	case "Fetch.requestPaused":
		var p struct {
			RequestID    string
			ResourceType string
			Request      struct{ URL string }
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return err
		}
		if p.Request.URL == "https://visual.invalid/" && p.ResourceType == "Document" && !c.documentServed {
			c.documentServed = true
			policy := "connect-src data: blob:"
			if c.allowNet {
				policy += " https:"
			}
			_, err := c.send("Fetch.fulfillRequest", map[string]any{"requestId": p.RequestID, "responseCode": 200, "responseHeaders": []map[string]string{{"name": "Content-Type", "value": "text/html; charset=utf-8"}, {"name": "Content-Security-Policy", "value": c.csp}, {"name": "Content-Security-Policy", "value": policy}}, "body": base64.StdEncoding.EncodeToString(c.doc)})
			return err
		}
		if p.Request.URL == "https://visual.invalid/favicon.ico" {
			_, err := c.send("Fetch.fulfillRequest", map[string]any{"requestId": p.RequestID, "responseCode": 204})
			return err
		}
		u := c.recordURL(p.Request.URL)
		if c.allowNet && u != nil && u.Scheme == "https" {
			_, err := c.send("Fetch.continueRequest", map[string]any{"requestId": p.RequestID})
			return err
		}
		c.blockedNetwork = true
		_, err := c.send("Fetch.failRequest", map[string]any{"requestId": p.RequestID, "errorReason": "BlockedByClient"})
		return err
	case "Runtime.consoleAPICalled":
		var p struct {
			Type string
			Args []struct {
				Value       any
				Description string
			}
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Type == "error" {
			var parts []string
			for _, a := range p.Args {
				if a.Value != nil {
					parts = append(parts, fmt.Sprint(a.Value))
				} else {
					parts = append(parts, a.Description)
				}
			}
			s := strings.Join(parts, " ")
			c.console = append(c.console, s)
		}
	case "Runtime.exceptionThrown":
		var p struct {
			ExceptionDetails struct {
				Text      string
				Exception struct{ Description string }
			}
		}
		_ = json.Unmarshal(m.Params, &p)
		s := p.ExceptionDetails.Exception.Description
		if s == "" {
			s = p.ExceptionDetails.Text
		}
		// Rejected fetch promises from blocked requests belong to the network check.
		if !(c.blockedNetwork && strings.HasPrefix(s, "TypeError: Failed to fetch") && strings.Contains(p.ExceptionDetails.Text, "in promise")) {
			c.console = append(c.console, s)
		}
	}
	return nil
}
func (c *client) evaluate(ctx context.Context, script string, out any) error {
	var r struct {
		Result           struct{ Value json.RawMessage }
		ExceptionDetails json.RawMessage
	}
	if err := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": script, "returnByValue": true, "awaitPromise": true}, &r); err != nil {
		return err
	}
	if len(r.ExceptionDetails) > 0 {
		return fmt.Errorf("evaluation failed: %s", r.ExceptionDetails)
	}
	return json.Unmarshal(r.Result.Value, out)
}

// recordURL excludes the synthetic document origin and inline resources.
func (c *client) recordURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	if u != nil && u.Host != "" && u.Host != "visual.invalid" && u.Scheme != "data" && u.Scheme != "blob" {
		c.hosts[u.Host] = true
	}
	return u
}

// screenshot captures the top of the page through the same CDP path for every image.
func (c *client) screenshot(ctx context.Context, width, height int, scale float64) ([]byte, error) {
	var shot struct{ Data string }
	err := c.call(ctx, "Page.captureScreenshot", map[string]any{"format": "webp", "quality": 82, "captureBeyondViewport": true, "clip": map[string]any{"x": 0, "y": 0, "width": width, "height": height, "scale": scale}}, &shot)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(shot.Data)
}
