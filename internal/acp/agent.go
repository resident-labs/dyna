package acp

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/jsonrpc"
)

// ClientInfo identifies Dyna to the agent.
var ClientInfo = map[string]any{"name": "dyna", "version": "1"}

// InitializeMeta is sent with initialize. Grok 1.0.62 only surfaces permission
// prompts to clients that declare an extension client type.
var InitializeMeta = map[string]any{"clientType": "extension"}

// CancelInterruptMeta marks Dyna's cancel as the interactive interrupt.
var CancelInterruptMeta = map[string]any{"cancelTrigger": "ctrl_c"}

// InitializeResult carries the negotiated agent capabilities.
type InitializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession        bool `json:"loadSession"`
		PromptCapabilities struct {
			Image           bool `json:"image"`
			Audio           bool `json:"audio"`
			EmbeddedContext bool `json:"embeddedContext"`
		} `json:"promptCapabilities"`
		SessionCapabilities struct {
			List   json.RawMessage `json:"list"`
			Resume json.RawMessage `json:"resume"`
			Close  json.RawMessage `json:"close"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AuthMethods []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Required bool   `json:"requiresAuthentication"`
	} `json:"authMethods"`
	Auth json.RawMessage `json:"auth"`
	Meta json.RawMessage `json:"_meta"`
}

// SessionSetup is the shared shape of session/new, session/load and
// session/resume replies.
type SessionSetup struct {
	SessionID string          `json:"sessionId"`
	Modes     json.RawMessage `json:"modes"`
	Models    json.RawMessage `json:"models"`
	Meta      json.RawMessage `json:"_meta"`
}

// ModelState is grok's session models extension on setup replies.
type ModelState struct {
	CurrentModelID  string `json:"currentModelId"`
	AvailableModels []struct {
		ModelID string          `json:"modelId"`
		Name    string          `json:"name"`
		Meta    json.RawMessage `json:"_meta"`
	} `json:"availableModels"`
}

// CurrentModel decodes the setup reply's models extension when present.
func (s SessionSetup) CurrentModel() (string, bool) {
	if len(s.Models) == 0 {
		return "", false
	}
	var state ModelState
	if json.Unmarshal(s.Models, &state) != nil || state.CurrentModelID == "" {
		return "", false
	}
	return state.CurrentModelID, true
}

// PromptBlock is one content block of a prompt.
type PromptBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextBlock builds a text prompt block.
func TextBlock(text string) PromptBlock { return PromptBlock{Type: "text", Text: text} }

// PromptResult is the reply to session/prompt.
type PromptResult struct {
	StopReason string          `json:"stopReason"`
	Meta       json.RawMessage `json:"_meta"`
}

// UsageMeta decodes grok's prompt-result usage fields.
type UsageMeta struct {
	TotalTokens int64  `json:"totalTokens"`
	ModelID     string `json:"modelId"`
	PromptID    string `json:"promptId"`
}

// Usage decodes the reply's usage metadata extension.
func (p PromptResult) Usage() UsageMeta {
	var u UsageMeta
	_ = json.Unmarshal(p.Meta, &u)
	return u
}

// Agent runs one ACP agent session conversation over a Conn.
type Agent struct {
	c       *jsonrpc.Conn
	Init    InitializeResult
	Session SessionSetup
	// SessionMeta carries provider session configuration on both creation and restore.
	SessionMeta map[string]any
}

// Handshake performs initialize + initialized on a fresh connection.
func Handshake(ctx context.Context, c *jsonrpc.Conn) (*Agent, error) {
	return HandshakeVersion(ctx, c, ProtocolVersion)
}

// ConnectedAgent attaches a request router to an already initialized child.
func ConnectedAgent(c *jsonrpc.Conn) *Agent { return &Agent{c: c} }

// HandshakeVersion offers the protocol version supported by the native agent.
func HandshakeVersion(ctx context.Context, c *jsonrpc.Conn, version int) (*Agent, error) {
	params := map[string]any{
		"protocolVersion": version,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": ClientInfo,
		"_meta":      InitializeMeta,
	}
	a := &Agent{c: c}
	if err := c.Request(ctx, "initialize", params, &a.Init); err != nil {
		return nil, fmt.Errorf("acp initialize: %w", err)
	}
	if err := c.Notify("initialized", struct{}{}); err != nil {
		return nil, err
	}
	return a, nil
}

// Authenticate runs the agent's authenticate request for one method id.
func (a *Agent) Authenticate(ctx context.Context, methodID string) error {
	if methodID == "" {
		return nil
	}
	return a.c.Request(ctx, "authenticate", map[string]any{"methodId": methodID}, nil)
}

// DefaultAuthMethod picks the agent's default authentication method id.
func (a *Agent) DefaultAuthMethod() string {
	if len(a.Init.AuthMethods) == 0 {
		return ""
	}
	return a.Init.AuthMethods[0].ID
}

// NewSession opens a new session in cwd. The session's MCP servers are
// per-session: grok spawns each with the session id in its environment.
func (a *Agent) NewSession(ctx context.Context, cwd string, mcpServers []McpServer) (SessionSetup, error) {
	var setup SessionSetup
	params := map[string]any{"cwd": cwd, "mcpServers": mcpServerParam(mcpServers)}
	if a.SessionMeta != nil {
		params["_meta"] = a.SessionMeta
	}
	if err := a.c.Request(ctx, "session/new", params, &setup); err != nil {
		return setup, fmt.Errorf("acp session/new: %w", err)
	}
	a.Session = setup
	return setup, nil
}

// LoadSession restores a persisted session (ACP v1 session/load). On a live
// agent process the restore is instant; a cold process replays history as
// notifications first, and replay observes them through onReplay instead of
// the turn's update stream.
func (a *Agent) LoadSession(ctx context.Context, sessionID, cwd string, mcpServers []McpServer) (SessionSetup, error) {
	var setup SessionSetup
	params := map[string]any{"sessionId": sessionID, "cwd": cwd, "mcpServers": mcpServerParam(mcpServers)}
	if a.SessionMeta != nil {
		params["_meta"] = a.SessionMeta
	}
	if err := a.c.Request(ctx, "session/load", params, &setup); err != nil {
		return setup, fmt.Errorf("acp session/load: %w", err)
	}
	a.Session = setup
	return setup, nil
}

// ResumeSession restores a session through the ACP v2 resume method.
func (a *Agent) ResumeSession(ctx context.Context, sessionID, cwd string, mcpServers []McpServer) (SessionSetup, error) {
	var setup SessionSetup
	params := map[string]any{"sessionId": sessionID, "cwd": cwd, "mcpServers": mcpServerParam(mcpServers)}
	if a.SessionMeta != nil {
		params["_meta"] = a.SessionMeta
	}
	if err := a.c.Request(ctx, "session/resume", params, &setup); err != nil {
		return setup, fmt.Errorf("acp session/resume: %w", err)
	}
	a.Session = setup
	return setup, nil
}

func mcpServerParam(servers []McpServer) any {
	if servers == nil {
		return []any{}
	}
	return servers
}

// Prompt sends one prompt and waits for its reply.
func (a *Agent) Prompt(ctx context.Context, sessionID string, blocks []PromptBlock) (PromptResult, error) {
	var result PromptResult
	params := map[string]any{"sessionId": sessionID, "prompt": blocks}
	if err := a.c.Request(ctx, "session/prompt", params, &result); err != nil {
		return result, fmt.Errorf("acp session/prompt: %w", err)
	}
	return result, nil
}

// Cancel asks the agent to interrupt the running prompt for sessionID.
func (a *Agent) Cancel(sessionID string) error {
	return a.c.Notify("session/cancel", map[string]any{"sessionId": sessionID, "_meta": CancelInterruptMeta})
}

// SetModel switches the session model (grok session/set_model extension).
func (a *Agent) SetModel(ctx context.Context, sessionID, modelID string) error {
	if modelID == "" {
		return nil
	}
	return a.c.Request(ctx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": modelID}, nil)
}

// SetMode selects the advertised native permission mode.
func (a *Agent) SetMode(ctx context.Context, sessionID, modeID string) error {
	return a.c.Request(ctx, "session/set_mode", map[string]any{"sessionId": sessionID, "modeId": modeID}, nil)
}
