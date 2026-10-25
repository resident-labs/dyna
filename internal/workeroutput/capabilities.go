package workeroutput

import "strings"

// DialectCapabilities describes the evidence available from a declared output
// stream. SessionResume means that the stream exposes a native session ID;
// the harness still owns the command used to resume that session.
type DialectCapabilities struct {
	Transcript    bool   `json:"transcript"`
	ToolEvents    bool   `json:"toolEvents"`
	SafePoints    bool   `json:"safePoints"`
	Usage         string `json:"usage"` // yes|unavailable|no
	UsageReason   string `json:"usageReason"`
	SessionResume bool   `json:"sessionResume"`
}

var dialectCapabilities = map[string]DialectCapabilities{
	DialectGrokJSON:         {Transcript: true, ToolEvents: true, SafePoints: true, Usage: "yes", SessionResume: true},
	DialectAcpJSON:          {Transcript: true, ToolEvents: true, SafePoints: true, Usage: "yes", SessionResume: true},
	DialectPiJSON:           {Transcript: true, ToolEvents: true, SafePoints: true, Usage: "yes", SessionResume: true},
	DialectText:             {Transcript: true, Usage: "no", UsageReason: "plain text output does not report usage"},
	DialectCodexJSONL:       {Transcript: true, ToolEvents: true, SafePoints: true, Usage: "yes", SessionResume: true},
	DialectClaudeStreamJSON: {Transcript: true, ToolEvents: true, SafePoints: true, Usage: "yes", SessionResume: true},
	DialectOpenCodeJSON:     {Transcript: true, ToolEvents: true, SafePoints: true, Usage: "yes", SessionResume: true},
	DialectMuseJSONL:        {Transcript: true, ToolEvents: true, SafePoints: true, Usage: "unavailable", UsageReason: "Muse JSON output does not report provider usage", SessionResume: true},
}

// Capabilities returns the output contract for a dialect. Unknown dialects
// expose no supported features. Conformance tests check every registered adapter.
func Capabilities(dialect string) DialectCapabilities {
	if c, ok := dialectCapabilities[strings.TrimSpace(dialect)]; ok {
		return c
	}
	return DialectCapabilities{Usage: "no", UsageReason: "unknown output dialect"}
}
