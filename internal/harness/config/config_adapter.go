package config

import (
	"sort"
	"sync"
)

// HarnessConfigCapabilities describes supported catalog generation features.
type HarnessConfigCapabilities struct {
	Harness      string `json:"harness"`
	MCP          bool   `json:"mcp"`
	Skills       bool   `json:"skills"`
	MCPReason    string `json:"mcpReason,omitempty"`
	SkillsReason string `json:"skillsReason,omitempty"`
}

// ConfigAdapter describes one harness's catalog features.
type ConfigAdapter interface {
	Capabilities() HarnessConfigCapabilities
}

var configAdapters = struct {
	sync.RWMutex
	values map[string]ConfigAdapter
}{values: map[string]ConfigAdapter{}}

// RegisterConfigAdapter installs an adapter, normally during package init.
// Registering an ID twice is a programming error.
func RegisterConfigAdapter(id string, adapter ConfigAdapter) {
	configAdapters.Lock()
	defer configAdapters.Unlock()
	if adapter == nil || id == "" || configAdapters.values[id] != nil {
		panic("invalid or duplicate harness config adapter")
	}
	configAdapters.values[id] = adapter
}

// ConfigCapabilities returns capabilities in harness ID order.
func ConfigCapabilities() []HarnessConfigCapabilities {
	configAdapters.RLock()
	defer configAdapters.RUnlock()
	out := make([]HarnessConfigCapabilities, 0, len(configAdapters.values))
	for id, adapter := range configAdapters.values {
		capability := adapter.Capabilities()
		capability.Harness = id
		out = append(out, capability)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Harness < out[j].Harness })
	return out
}

type nativeConfigAdapter struct{ harness string }

func (a nativeConfigAdapter) Capabilities() HarnessConfigCapabilities {
	return HarnessConfigCapabilities{Harness: a.harness, MCP: true, Skills: true}
}
