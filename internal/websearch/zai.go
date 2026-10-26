package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ZaiEndpoint is the z.ai GLM Coding Plan web search MCP server.
	ZaiEndpoint = "https://api.z.ai/api/mcp/web_search_prime/mcp"
	// zaiTool is the search tool the server exposes.
	zaiTool = "web_search_prime"
	// zaiProtocol is the MCP protocol version the server expects.
	zaiProtocol = "2025-03-26"
	// zaiSessionHeader carries the session id the server hands out.
	zaiSessionHeader = "Mcp-Session-Id"
	// zaiMaxBody caps one MCP answer.
	zaiMaxBody = 4 << 20
)

// zaiClient is how long one MCP call may take.
var zaiClient = &http.Client{Timeout: 20 * time.Second}

// Zai searches through the z.ai GLM Coding Plan web search MCP server. It
// speaks MCP streamable HTTP directly: one initialize call, then one
// tools/call on the session the first call opened. The credential is the
// owner's static coding-plan API key, not a sign-in token.
type Zai struct {
	// BaseURL is the MCP endpoint; empty uses ZaiEndpoint.
	BaseURL string
	// Key reads the owner's z.ai coding-plan API key, or returns
	// ErrNotSignedIn when the owner saved none.
	Key func(ctx context.Context) (string, error)
	// Client sends the request; nil uses a client with a 20 s timeout.
	Client *http.Client
}

// Name is "zai".
func (z Zai) Name() string { return "zai" }

// zaiItem is one search result the tool returns.
type zaiItem struct {
	Title       string `json:"title"`
	Link        string `json:"link"`
	Content     string `json:"content"`
	Refer       string `json:"refer"`
	PublishedAt string `json:"publishedAt"`
}

// zaiEnvelope is one JSON-RPC answer.
type zaiEnvelope struct {
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Search asks the MCP search tool about q.
func (z Zai) Search(ctx context.Context, q Query) ([]Result, error) {
	if z.Key == nil {
		return nil, ErrNotSignedIn
	}
	key, err := z.Key(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, ErrNotSignedIn
	}
	base := z.BaseURL
	if base == "" {
		base = ZaiEndpoint
	}
	endpoint := strings.TrimSuffix(base, "/")
	client := z.Client
	if client == nil {
		client = zaiClient
	}
	session, err := z.open(ctx, client, endpoint, key)
	if err != nil {
		return nil, err
	}
	answer, err := z.call(ctx, client, endpoint, key, session, zaiCall(q.Text))
	if err != nil {
		return nil, err
	}
	if answer.Error != nil {
		return nil, zaiError(answer.Error.Code, answer.Error.Message, key)
	}
	items, err := zaiItems(answer)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(items))
	for _, found := range items {
		if found.Link == "" {
			continue
		}
		results = append(results, Result{Title: text(found.Title), URL: found.Link, Snippet: text(found.Content)})
	}
	return capped(z.Name(), q, results), nil
}

// open runs the MCP initialize call and returns the session id it opened.
func (z Zai) open(ctx context.Context, client *http.Client, endpoint, key string) (string, error) {
	body := zaiInitialize()
	response, err := z.post(ctx, client, endpoint, key, "", body)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", statusError("zai", response.StatusCode, response.Body, key)
	}
	session := response.Header.Get(zaiSessionHeader)
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, zaiMaxBody))
	if session == "" {
		return "", errors.New("zai opened no MCP session")
	}
	return session, nil
}

// call runs one tools/call on an open session and returns its JSON-RPC answer.
func (z Zai) call(ctx context.Context, client *http.Client, endpoint, key, session string, body []byte) (zaiEnvelope, error) {
	response, err := z.post(ctx, client, endpoint, key, session, body)
	if err != nil {
		return zaiEnvelope{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return zaiEnvelope{}, statusError("zai", response.StatusCode, response.Body, key)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, zaiMaxBody))
	if err != nil {
		return zaiEnvelope{}, errors.New("zai did not finish its answer")
	}
	return zaiAnswer(raw)
}

// post sends one JSON-RPC call to the endpoint.
func (z Zai) post(ctx context.Context, client *http.Client, endpoint, key, session string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		request.Header.Set(zaiSessionHeader, session)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("zai request failed: %w", err)
	}
	return response, nil
}

// zaiInitialize is the MCP initialize call that opens a session.
func zaiInitialize() []byte {
	return []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
		`{"protocolVersion":"` + zaiProtocol + `","capabilities":{},` +
		`"clientInfo":{"name":"dyna","version":"1"}}}`)
}

// zaiCall is the tools/call that asks for the search. The tool schema refuses
// extra arguments and has no count field, so the limit stays a local cap.
func zaiCall(query string) []byte {
	text, err := json.Marshal(query)
	if err != nil {
		text = []byte(`""`)
	}
	return []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` +
		zaiTool + `","arguments":{"search_query":` + string(text) + `}}}`)
}

// zaiAnswer reads one JSON-RPC answer, from a text/event-stream frame or a
// plain JSON body.
func zaiAnswer(body []byte) (zaiEnvelope, error) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		payload, found := strings.CutPrefix(line, "data:")
		if !found {
			continue
		}
		var envelope zaiEnvelope
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &envelope); err != nil {
			continue
		}
		if envelope.Error != nil || len(envelope.Result.Content) > 0 {
			return envelope, nil
		}
	}
	var envelope zaiEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(body), &envelope); err != nil {
		return zaiEnvelope{}, errors.New("zai answered a response Dyna cannot read")
	}
	if envelope.Error == nil && len(envelope.Result.Content) == 0 {
		return zaiEnvelope{}, errors.New("zai answered no search")
	}
	return envelope, nil
}

// zaiItems reads the result items from the answer. The tool's text payload is
// a JSON document, sometimes quoted one extra time.
func zaiItems(answer zaiEnvelope) ([]zaiItem, error) {
	if len(answer.Result.Content) == 0 {
		return nil, errors.New("zai answered no search")
	}
	raw := []byte(strings.TrimSpace(answer.Result.Content[0].Text))
	for range 3 {
		var body struct {
			Results []zaiItem `json:"results"`
		}
		if err := json.Unmarshal(raw, &body); err == nil && body.Results != nil {
			return body.Results, nil
		}
		var list []zaiItem
		if err := json.Unmarshal(raw, &list); err == nil {
			return list, nil
		}
		var quoted string
		if err := json.Unmarshal(raw, &quoted); err != nil {
			break
		}
		raw = []byte(strings.TrimSpace(quoted))
	}
	return nil, errors.New("zai answered a response Dyna cannot read")
}

// zaiError reports a JSON-RPC failure with its code and a message that never
// carries the key.
func zaiError(code int, message, key string) error {
	message = text(strings.ReplaceAll(message, key, "[redacted]"))
	if message == "" {
		return fmt.Errorf("zai answered MCP error %d", code)
	}
	return fmt.Errorf("zai answered MCP error %d: %s", code, message)
}
