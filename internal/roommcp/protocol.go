package roommcp

import (
	"context"
	"encoding/json"
	"errors"
)

// Tool is a server-provided tool. Its description and schema are untrusted.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Initialize starts an MCP session with a fixed client identity.
func (c *Client) Initialize(ctx context.Context) error {
	params, err := json.Marshal(struct {
		ProtocolVersion string   `json:"protocolVersion"`
		Capabilities    struct{} `json:"capabilities"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}{ProtocolVersion: "2025-03-26", ClientInfo: struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}{Name: "dyna", Version: clientVersion}})
	if err != nil {
		return errors.New("MCP initialization failed")
	}
	raw, err := c.request(ctx, "initialize", params)
	if err != nil {
		return err
	}
	var result struct {
		Capabilities struct {
			Prompts json.RawMessage `json:"prompts"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return errors.New("invalid MCP initialization")
	}
	c.prompts = len(result.Capabilities.Prompts) > 0 && string(result.Capabilities.Prompts) != "null"
	_, err = c.request(ctx, "notifications/initialized", nil)
	return err
}

// ListTools reads every tools/list page, with a bound on server-controlled paging.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var tools []Tool
	cursor := ""
	seen := make(map[string]bool)
	for pages := 0; pages < 100; pages++ {
		params, _ := json.Marshal(struct {
			Cursor string `json:"cursor,omitempty"`
		}{Cursor: cursor})
		raw, err := c.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, errors.New("invalid MCP tool list")
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" {
			return tools, nil
		}
		if seen[page.NextCursor] {
			return nil, errors.New("invalid MCP tool list cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return nil, errors.New("MCP tool list has too many pages")
}

// CallTool sends arguments to a tool after the engine checks its policy.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (Result, error) {
	params, err := json.Marshal(struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: name, Arguments: arguments})
	if err != nil {
		return Result{}, errors.New("invalid MCP arguments")
	}
	raw, err := c.request(ctx, "tools/call", params)
	if err != nil {
		return Result{}, err
	}
	var result Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return Result{}, errors.New("invalid MCP result")
	}
	return result, nil
}

// Prompt describes a pinned, server-provided prompt.
type Prompt struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}
type PromptMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}
type PromptResult struct {
	Raw         json.RawMessage `json:"-"`
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// ListPrompts reads every page, bounded against a malicious server.
func (c *Client) ListPrompts(ctx context.Context) ([]Prompt, error) {
	var prompts []Prompt
	cursor := ""
	seen := make(map[string]bool)
	for pages := 0; pages < 100; pages++ {
		params, _ := json.Marshal(struct {
			Cursor string `json:"cursor,omitempty"`
		}{cursor})
		raw, err := c.request(ctx, "prompts/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Prompts    []Prompt `json:"prompts"`
			NextCursor string   `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil {
			return nil, errors.New("invalid MCP prompt list")
		}
		prompts = append(prompts, page.Prompts...)
		if page.NextCursor == "" {
			return prompts, nil
		}
		if seen[page.NextCursor] {
			return nil, errors.New("invalid MCP prompt list cursor")
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return nil, errors.New("MCP prompt list has too many pages")
}

// GetPrompt fetches a prompt after its arguments pass policy.
func (c *Client) GetPrompt(ctx context.Context, name string, arguments map[string]string) (PromptResult, error) {
	params, err := json.Marshal(struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}{name, arguments})
	if err != nil {
		return PromptResult{}, errors.New("invalid MCP arguments")
	}
	raw, err := c.request(ctx, "prompts/get", params)
	if err != nil {
		return PromptResult{}, err
	}
	var result PromptResult
	if json.Unmarshal(raw, &result) != nil {
		return PromptResult{}, errors.New("invalid MCP prompt result")
	}
	result.Raw = raw
	return result, nil
}
