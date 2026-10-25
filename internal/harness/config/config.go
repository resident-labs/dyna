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

var ConfigHarnesses = []string{"claude-code", "opencode", "grok", "pi", "muse", "kimi-code"}

type MCPDefinition struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	EnvKeys []string `json:"envKeys,omitempty"`
	URL     string   `json:"url,omitempty"`
}

type ManagedMCP struct {
	Name string `json:"name"`
	MCPDefinition
	EnabledHarnesses []string `json:"enabledHarnesses"`
}

type ManagedSkill struct {
	Name             string   `json:"name"`
	Source           string   `json:"source"`
	EnabledHarnesses []string `json:"enabledHarnesses"`
}
type HarnessConfig struct {
	ToolHomeBridges     []string       `json:"toolHomeBridges,omitempty"`
	DefaultSkillsSeeded bool           `json:"defaultSkillsSeeded,omitempty"`
	MCP                 []ManagedMCP   `json:"mcp"`
	Skills              []ManagedSkill `json:"skills"`
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
	c := HarnessConfig{MCP: []ManagedMCP{}, Skills: []ManagedSkill{}}
	err := store.ReadJSON(m.Path, &c)
	return c, err
}
func (m *ConfigManager) update(fn func(*HarnessConfig) error) error {
	if err := os.MkdirAll(filepath.Dir(m.Path), 0700); err != nil {
		return err
	}
	c := HarnessConfig{}
	return store.Update(m.Path+".lock", m.Path, &c, func() error { return fn(&c) })
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
	for _, x := range c.Skills {
		if harness == "" || slices.Contains(x.EnabledHarnesses, hs[0]) {
			out.Skills = append(out.Skills, x)
		}
	}
	return out, nil
}

// RetireManagedSkill removes an entry Dyna once seeded into the catalog. It
// matches the source path so an owner-added skill with the same name stays.
// TODO: legacy/shim remove later — drops the orchestrator skill older versions seeded into every harness; remove once supported catalogs no longer hold it
func (m *ConfigManager) RetireManagedSkill(name, source string) error {
	if _, err := os.Stat(m.Path); err != nil {
		return nil
	}
	return m.update(func(c *HarnessConfig) error {
		c.DefaultSkillsSeeded = false
		c.Skills = slices.DeleteFunc(c.Skills, func(s ManagedSkill) bool { return s.Name == name && s.Source == source })
		return nil
	})
}
