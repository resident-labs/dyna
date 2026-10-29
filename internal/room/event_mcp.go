package room

// FamilyMCP records calls to room-scoped external MCP servers.
const FamilyMCP EventFamily = "mcp"

// MCPOutcome is the result of a call or policy check.
type MCPOutcome string

const (
	MCPOk          MCPOutcome = "ok"
	MCPServerError MCPOutcome = "server-error"
	MCPNotAllowed  MCPOutcome = "not-allowed"
	MCPNotGranted  MCPOutcome = "not-granted"
	MCPFlagged     MCPOutcome = "flagged"
	MCPRateLimited MCPOutcome = "rate-limited"
	MCPDetached    MCPOutcome = "detached"
)

var mcpOutcomes = []MCPOutcome{MCPOk, MCPServerError, MCPNotAllowed, MCPNotGranted, MCPFlagged, MCPRateLimited, MCPDetached}

// MCPCallEvent records one call. Arguments must be scrubbed and capped first.
type MCPCallEvent struct {
	Server      string     `json:"server"`
	Tool        string     `json:"tool"`
	Session     string     `json:"session"`
	Arguments   string     `json:"arguments,omitempty"`
	ResultBytes int        `json:"resultBytes,omitempty"`
	DurationMs  int64      `json:"durationMs,omitempty"`
	Outcome     MCPOutcome `json:"outcome"`
}

func (MCPCallEvent) eventFamily() EventFamily { return FamilyMCP }
func (MCPCallEvent) eventType() EventType     { return TypeResult }
func (p MCPCallEvent) eventSubject() string   { return p.Server }
func (MCPCallEvent) eventDedupe() string      { return "" }
