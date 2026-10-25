package acp

import (
	"context"
	"encoding/json"
	"fmt"

	"dyna-agent/internal/jsonrpc"
)

// ClientInfo identifies Dyna to an ACP agent on initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DynaClientInfo is the client identity Dyna sends on initialize.
var DynaClientInfo = ClientInfo{Name: "dyna", Version: "1"}

// FileSystemCapabilities declares the file methods the client serves. Dyna
// serves none of them itself, so both fields stay false.
type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

// ClientCapabilities is what Dyna offers an agent on initialize.
type ClientCapabilities struct {
	FS       FileSystemCapabilities `json:"fs"`
	Terminal bool                   `json:"terminal"`
}

// ClientMeta is client extension data on initialize. Grok 1.0.62 only surfaces
// permission prompts to clients that declare an extension client type.
type ClientMeta struct {
	ClientType string `json:"clientType"`
}

// ExtensionClientType declares a client that reads agent extensions.
const ExtensionClientType = "extension"

// InitializeParams is the initialize request.
type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         ClientInfo         `json:"clientInfo"`
	Meta               ClientMeta         `json:"_meta"`
}

// CancelMeta marks Dyna's cancel as the interactive interrupt.
type CancelMeta struct {
	CancelTrigger string `json:"cancelTrigger"`
}

// CancelTriggerInterrupt is the interrupt trigger Dyna reports on cancel.
const CancelTriggerInterrupt = "ctrl_c"

// CancelParams is the session/cancel notification.
type CancelParams struct {
	SessionID string     `json:"sessionId"`
	Meta      CancelMeta `json:"_meta"`
}

// AuthenticateParams is the authenticate request.
type AuthenticateParams struct {
	MethodID string `json:"methodId"`
}

// SessionParams is the shared shape of the session/new, session/load and
// session/resume requests. Meta carries the provider's raw extension data.
type SessionParams struct {
	SessionID  string          `json:"sessionId,omitempty"`
	Cwd        string          `json:"cwd"`
	McpServers []McpServer     `json:"mcpServers"`
	Meta       json.RawMessage `json:"_meta,omitempty"`
}

// PromptParams is the session/prompt request.
type PromptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []PromptBlock `json:"prompt"`
}

// SetModelParams is grok's session/set_model request.
type SetModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// SetModeParams is the session/set_mode request.
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SetConfigOptionParams is the session/set_config_option request.
type SetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

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
	SessionID     string                `json:"sessionId"`
	Modes         json.RawMessage       `json:"modes"`
	Models        json.RawMessage       `json:"models"`
	ConfigOptions []SessionConfigOption `json:"configOptions"`
	Meta          json.RawMessage       `json:"_meta"`
}

// SessionConfigOption is one model, thinking, or mode selector returned by ACP.
type SessionConfigOption struct {
	Type         string                      `json:"type"`
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Category     string                      `json:"category"`
	CurrentValue string                      `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options"`
}

// SessionConfigSelectOption is one allowed value of a select option.
type SessionConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description"`
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
	if len(s.Models) != 0 {
		var state ModelState
		if json.Unmarshal(s.Models, &state) == nil && state.CurrentModelID != "" {
			return state.CurrentModelID, true
		}
	}
	for _, option := range s.ConfigOptions {
		if option.ID == "model" && option.CurrentValue != "" {
			return option.CurrentValue, true
		}
	}
	return "", false
}

// CurrentThinking reads Kimi's thought_level config option.
func (s SessionSetup) CurrentThinking() (string, bool) {
	for _, option := range s.ConfigOptions {
		if option.ID == "thinking" && option.CurrentValue != "" {
			return option.CurrentValue, true
		}
	}
	return "", false
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
	// SessionMeta carries provider extension data on both session creation and
	// restore. Agents reject fields they do not know, so each provider's
	// adapter owns this shape and passes it as raw JSON.
	SessionMeta json.RawMessage
}

// Handshake performs initialize + initialized on a fresh connection.
func Handshake(ctx context.Context, c *jsonrpc.Conn) (*Agent, error) {
	return HandshakeVersion(ctx, c, ProtocolVersion)
}

// ConnectedAgent attaches a request router to an already initialized child.
func ConnectedAgent(c *jsonrpc.Conn) *Agent { return &Agent{c: c} }

// HandshakeVersion offers the protocol version supported by the native agent.
func HandshakeVersion(ctx context.Context, c *jsonrpc.Conn, version int) (*Agent, error) {
	params := InitializeParams{
		ProtocolVersion:    version,
		ClientCapabilities: ClientCapabilities{FS: FileSystemCapabilities{}, Terminal: false},
		ClientInfo:         DynaClientInfo,
		Meta:               ClientMeta{ClientType: ExtensionClientType},
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
	return a.c.Request(ctx, "authenticate", AuthenticateParams{MethodID: methodID}, nil)
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
	if err := a.c.Request(ctx, "session/new", a.sessionParams("", cwd, mcpServers), &setup); err != nil {
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
	if err := a.c.Request(ctx, "session/load", a.sessionParams(sessionID, cwd, mcpServers), &setup); err != nil {
		return setup, fmt.Errorf("acp session/load: %w", err)
	}
	a.Session = setup
	return setup, nil
}

// ResumeSession restores a session through the ACP v2 resume method.
func (a *Agent) ResumeSession(ctx context.Context, sessionID, cwd string, mcpServers []McpServer) (SessionSetup, error) {
	var setup SessionSetup
	if err := a.c.Request(ctx, "session/resume", a.sessionParams(sessionID, cwd, mcpServers), &setup); err != nil {
		return setup, fmt.Errorf("acp session/resume: %w", err)
	}
	a.Session = setup
	return setup, nil
}

// sessionParams builds a session request. An absent MCP list must reach the
// agent as an empty array, not null, and provider meta only when it is set.
func (a *Agent) sessionParams(sessionID, cwd string, servers []McpServer) SessionParams {
	if servers == nil {
		servers = []McpServer{}
	}
	return SessionParams{SessionID: sessionID, Cwd: cwd, McpServers: servers, Meta: a.SessionMeta}
}

// Prompt sends one prompt and waits for its reply.
func (a *Agent) Prompt(ctx context.Context, sessionID string, blocks []PromptBlock) (PromptResult, error) {
	var result PromptResult
	params := PromptParams{SessionID: sessionID, Prompt: blocks}
	if err := a.c.Request(ctx, "session/prompt", params, &result); err != nil {
		return result, fmt.Errorf("acp session/prompt: %w", err)
	}
	return result, nil
}

// Cancel asks the agent to interrupt the running prompt for sessionID.
func (a *Agent) Cancel(sessionID string) error {
	return a.c.Notify("session/cancel", CancelParams{SessionID: sessionID, Meta: CancelMeta{CancelTrigger: CancelTriggerInterrupt}})
}

// SetModel switches the session model (grok session/set_model extension).
func (a *Agent) SetModel(ctx context.Context, sessionID, modelID string) error {
	if modelID == "" {
		return nil
	}
	return a.c.Request(ctx, "session/set_model", SetModelParams{SessionID: sessionID, ModelID: modelID}, nil)
}

// SetMode selects the advertised native permission mode.
func (a *Agent) SetMode(ctx context.Context, sessionID, modeID string) error {
	return a.c.Request(ctx, "session/set_mode", SetModeParams{SessionID: sessionID, ModeID: modeID}, nil)
}

// SetConfigOption changes one ACP session config option, such as thinking.
func (a *Agent) SetConfigOption(ctx context.Context, sessionID, configID, value string) error {
	params := SetConfigOptionParams{SessionID: sessionID, ConfigID: configID, Value: value}
	return a.c.Request(ctx, "session/set_config_option", params, nil)
}
