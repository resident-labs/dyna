// Package roommcp contains the engine-side client for room MCP servers.
package roommcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes = 8 << 20
	// clientVersion is part of the fixed identity every server sees.
	clientVersion = "1"
	// maxRetryAfter caps how long one 429 answer can hold the client.
	maxRetryAfter = time.Hour
)

// Client uses a fixed identity and keeps the session ID returned by the server.
type Client struct {
	url        *url.URL
	token      string
	http       *http.Client
	mu         sync.Mutex
	session    string
	prompts    bool
	retryUntil time.Time
	now        func() time.Time
}

// ServerError is a JSON-RPC error answer. Its text comes from the server, so it
// is untrusted and must be wrapped before an agent sees it.
type ServerError struct{ Message string }

func (e *ServerError) Error() string { return "MCP server error" }

// NewClient returns a client for an HTTPS server, or a loopback server for tests.
func NewClient(rawURL, token string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid MCP server URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return nil, errors.New("MCP server URL must use HTTPS")
	}
	if token == "" {
		return nil, errors.New("save the token first")
	}
	c := &Client{url: u, token: token, now: time.Now}
	c.http = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !strings.EqualFold(req.URL.Scheme, u.Scheme) || !strings.EqualFold(req.URL.Host, u.Host) {
			return errors.New("MCP cross-origin redirect refused")
		}
		return nil
	}}
	return c, nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) request(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	blocked := c.now().Before(c.retryUntil)
	c.mu.Unlock()
	if blocked {
		return nil, errors.New("MCP server is rate limited")
	}
	id := 1
	var requestID *int
	if !strings.HasPrefix(method, "notifications/") {
		requestID = &id
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: requestID, Method: method, Params: params})
	if err != nil {
		return nil, errors.New("invalid MCP request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid MCP request")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "dyna")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("MCP request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		now := c.now()
		header := resp.Header.Get("Retry-After")
		until, err := http.ParseTime(header)
		if seconds, parseErr := strconv.ParseInt(header, 10, 32); parseErr == nil && seconds >= 0 {
			until = now.Add(time.Duration(seconds) * time.Second)
			err = nil
		}
		if limit := now.Add(maxRetryAfter); until.After(limit) {
			until = limit
		}
		if err == nil && until.After(now) {
			c.mu.Lock()
			if until.After(c.retryUntil) {
				c.retryUntil = until
			}
			c.mu.Unlock()
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP server returned HTTP %d", resp.StatusCode)
	}
	if sessionID := resp.Header.Get("Mcp-Session-Id"); sessionID != "" {
		c.mu.Lock()
		c.session = sessionID
		c.mu.Unlock()
	}
	if requestID == nil {
		return nil, nil
	}
	var result rpcResponse
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxResponseBytes+1))
		scanner.Buffer(make([]byte, 4096), maxResponseBytes)
		var event []string
		found := false
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if len(event) > 0 && json.Unmarshal([]byte(strings.Join(event, "\n")), &result) == nil && result.ID != nil && *result.ID == *requestID {
					found = true
					break
				}
				event = nil
				continue
			}
			if strings.HasPrefix(line, "data:") {
				event = append(event, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if !found {
			return nil, errors.New("invalid MCP event stream")
		}
	} else {
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil || len(data) > maxResponseBytes || json.Unmarshal(data, &result) != nil || result.ID == nil || *result.ID != *requestID {
			return nil, errors.New("invalid MCP response")
		}
	}
	if result.Error != nil {
		return nil, &ServerError{Message: Scrub(result.Error.Message, []string{c.token})}
	}
	if len(result.Result) == 0 {
		return nil, errors.New("MCP response has no result")
	}
	return result.Result, nil
}
