package config

type MCPDefinition struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	EnvKeys []string `json:"envKeys,omitempty"`
	URL     string   `json:"url,omitempty"`
	// TimeoutSeconds is one server's request timeout for harnesses that take
	// one, such as Pi. Zero keeps the harness default.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Exposure is how Pi exposes this server's tools to the model. Empty keeps
	// Pi's default, codemode.
	Exposure MCPExposure `json:"exposure,omitempty"`
}

// MCPExposure is how Pi reaches one MCP server's tools. Dyna passes it through
// as the server's exposure, so the values are Pi's names for them.
type MCPExposure string

// Pi's server exposures. Codemode is Pi's default when the field is empty.
const (
	MCPExposureCodemode         MCPExposure = "codemode"
	MCPExposureCodemodeDeferred MCPExposure = "codemode-deferred"
	MCPExposureDeferred         MCPExposure = "deferred"
	MCPExposureDirect           MCPExposure = "direct"
	MCPExposureHidden           MCPExposure = "hidden"
)

type ManagedMCP struct {
	Name             string `json:"name"`
	MCPDefinition    `tstype:",extends"`
	EnabledHarnesses []string `json:"enabledHarnesses" tstype:"string[] | null"`
}

// ManagedSkill is one entry in Dyna's skill catalog. The catalog is global: an
// enabled skill reaches every session through its standing instructions, and
// Dyna never installs it into a harness home.
type ManagedSkill struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Enabled bool   `json:"enabled"`
}

type HarnessConfig struct {
	ToolHomeBridges []string       `json:"toolHomeBridges,omitempty"`
	MCP             []ManagedMCP   `json:"mcp" tstype:"ManagedMCP[] | null"`
	Skills          []ManagedSkill `json:"skills" tstype:"ManagedSkill[] | null"`
}
