package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/resident-labs/dyna/box/browser"
)

type target struct {
	Type      string `json:"type"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}
type message struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// DevTools is a single sequential page connection.
type DevTools struct {
	conn   *websocket.Conn
	id     int
	loaded bool
}

// ConnectTimeoutError reports that Chromium did not expose a page in time.
type ConnectTimeoutError struct{}

func (ConnectTimeoutError) Error() string { return "timed out connecting to Chromium" }

// Connect waits for Chromium to expose its first page target.
func Connect(ctx context.Context, base string) (*DevTools, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return connect(ctx, base)
}

func connect(ctx context.Context, base string) (_ *DevTools, resultErr error) {
	defer func() {
		if resultErr != nil && ctx.Err() == context.DeadlineExceeded {
			resultErr = ConnectTimeoutError{}
		}
	}()
	for {
		var targets []target
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/list", nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&targets)
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
			for _, t := range targets {
				if t.Type == "page" {
					conn, _, err := websocket.Dial(ctx, t.WebSocket, nil)
					if err != nil {
						return nil, err
					}
					conn.SetReadLimit(64 << 20)
					return &DevTools{conn: conn}, nil
				}
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (d *DevTools) Close() { _ = d.conn.CloseNow() }

type devToolsProtoError struct {
	method  string
	message string
}

func (e *devToolsProtoError) Error() string {
	return fmt.Sprintf("DevTools %s: %s", e.method, e.message)
}

func isDevToolsProtoError(err error) bool {
	var pe *devToolsProtoError
	return errors.As(err, &pe)
}

// Call sends one protocol command and returns its result.
func (d *DevTools) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if method == "Page.navigate" {
		d.loaded = false
	}
	d.id++
	payload, err := json.Marshal(message{ID: d.id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	if err = d.conn.Write(ctx, websocket.MessageText, payload); err != nil {
		return nil, err
	}
	for {
		_, data, err := d.conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		var msg message
		if err = json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		if msg.Method == "Page.loadEventFired" {
			d.loaded = true
		}
		if msg.ID != d.id {
			continue
		}
		if msg.Error != nil {
			return nil, &devToolsProtoError{method: method, message: msg.Error.Message}
		}
		return msg.Result, nil
	}
}

// WaitLoad waits for the load event, including one received during Navigate.
func (d *DevTools) WaitLoad(ctx context.Context) error {
	for !d.loaded {
		_, data, err := d.conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg message
		if err = json.Unmarshal(data, &msg); err != nil {
			return err
		}
		if msg.Method == "Page.loadEventFired" {
			d.loaded = true
		}
	}
	return nil
}

// GetCookies reads all browser cookies via Storage.getCookies.
// Errors never include cookie values.
func (d *DevTools) GetCookies(ctx context.Context) ([]browser.Cookie, error) {
	rawResult, err := d.Call(ctx, "Storage.getCookies", json.RawMessage(`{}`))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("DevTools Storage.getCookies failed")
	}
	var payload struct {
		Cookies []json.RawMessage `json:"cookies"`
	}
	if err := json.Unmarshal(rawResult, &payload); err != nil {
		return nil, errors.New("decode Storage.getCookies response")
	}
	out := make([]browser.Cookie, 0, len(payload.Cookies))
	skipped := 0
	for _, raw := range payload.Cookies {
		c, err := browser.ParseCookie(raw)
		if err != nil {
			skipped++
			continue
		}
		out = append(out, c)
	}
	if skipped > 0 {
		log.Printf("computer: DevTools Storage.getCookies skipped %d invalid cookies", skipped)
	}
	return out, nil
}

// SetCookies applies cookies via Storage.setCookies, falling back to per-cookie calls if Chromium
// rejects a cookie in the batch. Protocol-level rejections of individual cookies are logged by count
// only (never values) and skipped so one bad cookie cannot stall sync; transport/context errors fail.
func (d *DevTools) SetCookies(ctx context.Context, cookies []browser.Cookie) error {
	if len(cookies) == 0 {
		return nil
	}
	params, err := json.Marshal(struct {
		Cookies []browser.Cookie `json:"cookies"`
	}{Cookies: cookies})
	if err != nil {
		return errors.New("encode Storage.setCookies request")
	}
	_, err = d.Call(ctx, "Storage.setCookies", params)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !isDevToolsProtoError(err) {
		return errors.New("DevTools Storage.setCookies connection failed")
	}
	if len(cookies) == 1 {
		log.Printf("computer: DevTools Storage.setCookies skipped 1 rejected cookie")
		return nil
	}
	skipped := 0
	for _, c := range cookies {
		one, err := json.Marshal(struct {
			Cookies []browser.Cookie `json:"cookies"`
		}{Cookies: []browser.Cookie{c}})
		if err != nil {
			skipped++
			continue
		}
		if _, err := d.Call(ctx, "Storage.setCookies", one); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !isDevToolsProtoError(err) {
				return errors.New("DevTools Storage.setCookies connection failed")
			}
			skipped++
		}
	}
	if skipped > 0 {
		log.Printf("computer: DevTools Storage.setCookies skipped %d of %d rejected cookies", skipped, len(cookies))
	}
	return nil
}

// DeleteCookies removes cookies matching keys via Network.deleteCookies.
// Protocol-level rejections are logged by count only; transport/context errors fail.
func (d *DevTools) DeleteCookies(ctx context.Context, keys []browser.CookieKey) error {
	if len(keys) == 0 {
		return nil
	}
	skipped := 0
	for _, k := range keys {
		params := struct {
			Name         string          `json:"name"`
			Domain       string          `json:"domain"`
			Path         string          `json:"path"`
			PartitionKey json.RawMessage `json:"partitionKey,omitempty"`
		}{
			Name:   k.Name,
			Domain: k.Domain,
			Path:   k.Path,
		}
		if k.HasPartitionKey() {
			params.PartitionKey = k.PartitionKey
		}
		raw, err := json.Marshal(params)
		if err != nil {
			skipped++
			continue
		}
		if _, err := d.Call(ctx, "Network.deleteCookies", raw); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !isDevToolsProtoError(err) {
				return errors.New("DevTools Network.deleteCookies connection failed")
			}
			skipped++
		}
	}
	if skipped > 0 {
		log.Printf("computer: DevTools Network.deleteCookies skipped %d of %d rejected deletes", skipped, len(keys))
	}
	return nil
}
