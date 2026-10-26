// Package config owns Dyna's harness config catalog: MCP servers, skills
// and tool bridges. It does not run agent turns or authenticate accounts.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/store"
)

var ConfigHarnesses = []string{"claude-code", "grok", "pi", "kimi-code"}

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

// Valid reports a known Pi exposure, or the empty default.
func (e MCPExposure) Valid() bool {
	switch e {
	case "", MCPExposureCodemode, MCPExposureCodemodeDeferred, MCPExposureDeferred, MCPExposureDirect, MCPExposureHidden:
		return true
	}
	return false
}

type ManagedMCP struct {
	Name string `json:"name"`
	MCPDefinition
	EnabledHarnesses []string `json:"enabledHarnesses"`
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
	MCP             []ManagedMCP   `json:"mcp"`
	Skills          []ManagedSkill `json:"skills"`
}

// harnessConfigFile is the on-disk catalog document. Older versions scoped each
// skill to harnesses with enabledHarnesses; read turns that scope into the one
// Enabled flag, and the next update writes the current shape without it.
type harnessConfigFile struct {
	ToolHomeBridges []string     `json:"toolHomeBridges,omitempty"`
	MCP             []ManagedMCP `json:"mcp"`
	Skills          []fileSkill  `json:"skills"`
}

type fileSkill struct {
	Name             string   `json:"name"`
	Source           string   `json:"source"`
	Enabled          bool     `json:"enabled"`
	EnabledHarnesses []string `json:"enabledHarnesses,omitempty"`
}

func (f harnessConfigFile) config() HarnessConfig {
	c := HarnessConfig{ToolHomeBridges: f.ToolHomeBridges, MCP: append([]ManagedMCP{}, f.MCP...), Skills: []ManagedSkill{}}
	for _, s := range f.Skills {
		c.Skills = append(c.Skills, ManagedSkill{Name: s.Name, Source: s.Source, Enabled: s.Enabled || len(s.EnabledHarnesses) > 0})
	}
	return c
}

func (f *harnessConfigFile) fill(c HarnessConfig) {
	f.ToolHomeBridges, f.MCP = c.ToolHomeBridges, c.MCP
	f.Skills = nil
	for _, s := range c.Skills {
		f.Skills = append(f.Skills, fileSkill{Name: s.Name, Source: s.Source, Enabled: s.Enabled})
	}
}

type ConfigSnapshot struct {
	Capabilities []HarnessConfigCapabilities `json:"capabilities"`
	MCP          []ManagedMCP                `json:"mcp"`
	Skills       []ManagedSkill              `json:"skills"`
}

// ConfigManager separates Dyna's document from the harness files. Reads never
// create harness directories or write harness files.
type ConfigManager struct{ Home, Path string }

func NewConfigManager() *ConfigManager {
	home, _ := os.UserHomeDir()
	return &ConfigManager{Home: home, Path: filepath.Join(store.ConfigDir(), "harness-config.json")}
}
func (m *ConfigManager) Read() (HarnessConfig, error) {
	var f harnessConfigFile
	err := store.ReadJSON(m.Path, &f)
	return f.config(), err
}
func (m *ConfigManager) update(fn func(*HarnessConfig) error) error {
	if err := os.MkdirAll(filepath.Dir(m.Path), 0700); err != nil {
		return err
	}
	var f harnessConfigFile
	return store.Update(m.Path+".lock", m.Path, &f, func() error {
		c := f.config()
		if err := fn(&c); err != nil {
			return err
		}
		f.fill(c)
		return nil
	})
}
func selectedHarnesses(h string) ([]string, error) {
	if h == "" {
		return slices.Clone(ConfigHarnesses), nil
	}
	h = CanonicalHarnessID(h)
	if !slices.Contains(ConfigHarnesses, h) {
		return nil, fmt.Errorf("unknown harness %q; use a supported harness ID", h)
	}
	return []string{h}, nil
}
func validKind(k string) error {
	if k != "mcp" && k != "skill" {
		return fmt.Errorf("unknown config kind %q", k)
	}
	return nil
}

// safeMCPDefinition rejects embedded credentials in catalog definitions.
// The catalog editor uses this check before private generation.
func safeMCPDefinition(d MCPDefinition) bool {
	if u, err := url.Parse(d.URL); err == nil && d.URL != "" {
		if u.User != nil {
			return false
		}
		for key := range u.Query() {
			if credentialKey(key) {
				return false
			}
		}
	}
	for i, arg := range d.Args {
		if u, e := url.Parse(arg); e == nil && u.Scheme != "" {
			if u.User != nil {
				return false
			}
			for key := range u.Query() {
				if credentialKey(key) {
					return false
				}
			}
		}
		key, value, hasValue := strings.Cut(arg, "=")
		if credentialKey(strings.TrimLeft(key, "-")) && (hasValue && value != "" || !hasValue && i+1 < len(d.Args)) {
			return false
		}
		if lower := strings.ToLower(arg); strings.Contains(lower, "bearer ") || strings.Contains(lower, "authorization:") || strings.Contains(lower, "x-api-key:") {
			return false
		}
	}
	return true
}
func credentialKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	if key == "key" {
		return true
	}
	for _, suffix := range []string{"file", "path", "ref"} {
		if strings.HasSuffix(key, suffix) {
			return false
		}
	}
	for _, part := range []string{"token", "password", "secret", "apikey", "authorization", "credential"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

// Catalog reads only Dyna's source of truth; it never inspects personal MCPs or skills.
// MCP entries can be scoped to one harness; the skill catalog is global.
func (m *ConfigManager) Catalog(harness string) (ConfigSnapshot, error) {
	hs, err := selectedHarnesses(harness)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	c, err := m.Read()
	if err != nil {
		return ConfigSnapshot{}, err
	}
	out := ConfigSnapshot{MCP: []ManagedMCP{}, Skills: []ManagedSkill{}, Capabilities: ConfigCapabilities()}
	for _, x := range c.MCP {
		if harness == "" || slices.Contains(x.EnabledHarnesses, hs[0]) {
			out.MCP = append(out.MCP, x)
		}
	}
	out.Skills = append(out.Skills, c.Skills...)
	return out, nil
}
