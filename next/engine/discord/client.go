// Package discord implements the control-plane Discord connection. Credentials never enter a box.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	HTTP            *http.Client
	Endpoint, Token string
}
type Result struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// API validates the REST boundary and never follows a redirect with credentials.
func (c *Client) API(ctx context.Context, method, path string, body json.RawMessage, reason string) (Result, error) {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return Result{}, errors.New("unsupported Discord method")
	}
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/api/v10/") || strings.Contains(u.Path, "..") || strings.ContainsAny(u.Path, "\\\r\n") {
		return Result{}, errors.New("path must be under /api/v10/")
	}
	if method != "GET" && (u.Path == "/api/v10/users/@me" || strings.HasPrefix(u.Path, "/api/v10/users/@me/")) {
		return Result{}, errors.New("bot account writes are refused")
	}
	if len(body) > 0 && !json.Valid(body) {
		return Result{}, errors.New("invalid JSON body")
	}
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Endpoint, "/")+u.String(), bytes.NewReader(body))
		if err != nil {
			return Result{}, errors.New("invalid Discord request")
		}
		req.Header.Set("Authorization", "Bot "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		if reason != "" {
			req.Header.Set("X-Audit-Log-Reason", url.QueryEscape(reason))
		}
		resp, err := client.Do(req)
		if err != nil {
			return Result{}, errors.New("Discord request unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return Result{}, errors.New("cannot read Discord response")
		}
		if resp.StatusCode == 429 && attempt == 0 {
			var rate struct {
				RetryAfter float64 `json:"retry_after"`
			}
			if json.Unmarshal(data, &rate) != nil || rate.RetryAfter <= 0 {
				return Result{}, errors.New("invalid Discord rate limit")
			}
			timer := time.NewTimer(time.Duration(rate.RetryAfter * float64(time.Second)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return Result{}, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		// Discord responses are untrusted: never return a reflected credential.
		data = bytes.ReplaceAll(data, []byte(c.Token), []byte("[redacted]"))
		if len(data) > 20*1024 {
			data, _ = json.Marshal(string(data[:20*1024]) + " [truncated]")
		}
		if len(data) == 0 {
			data = []byte("null")
		}
		if !json.Valid(data) {
			data, _ = json.Marshal(string(data))
		}
		return Result{Status: resp.StatusCode, Body: data}, nil
	}
	return Result{}, errors.New("Discord rate limit")
}

type Identity struct {
	ID   string `json:"id"`
	Name string `json:"username"`
}
type Guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (c *Client) Validate(ctx context.Context) (Identity, Guild, error) {
	if strings.TrimSpace(c.Token) == "" || strings.ContainsAny(c.Token, "\r\n") {
		return Identity{}, Guild{}, errors.New("invalid Discord token")
	}
	user, err := c.API(ctx, "GET", "/api/v10/users/@me", nil, "")
	if err != nil {
		return Identity{}, Guild{}, err
	}
	if user.Status != 200 {
		return Identity{}, Guild{}, fmt.Errorf("Discord rejected token (HTTP %d)", user.Status)
	}
	var bot Identity
	if json.Unmarshal(user.Body, &bot) != nil || bot.ID == "" {
		return Identity{}, Guild{}, errors.New("invalid Discord bot response")
	}
	result, err := c.API(ctx, "GET", "/api/v10/users/@me/guilds", nil, "")
	if err != nil {
		return Identity{}, Guild{}, err
	}
	if result.Status != 200 {
		return Identity{}, Guild{}, errors.New("cannot list Discord servers")
	}
	var guilds []Guild
	if json.Unmarshal(result.Body, &guilds) != nil {
		return Identity{}, Guild{}, errors.New("invalid Discord server response")
	}
	if len(guilds) != 1 {
		return Identity{}, Guild{}, fmt.Errorf("bot must be in exactly one server; found %d", len(guilds))
	}
	return bot, guilds[0], nil
}
