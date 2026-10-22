package config

import (
	"fmt"
	"regexp"
	"strings"
)

var toolHomeBridgeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// ToolHomeEnvironment replaces inherited tool bridge opt-ins with owner policy
// from harness-config.json. Prepare always supplies the dyna and git defaults.
// This method reads configuration only and does not change the input slice.
func (m *ConfigManager) ToolHomeEnvironment(inherited []string) ([]string, error) {
	config, err := m.Read()
	if err != nil {
		return nil, fmt.Errorf("read tool HOME bridge policy: %w", err)
	}
	for _, name := range config.ToolHomeBridges {
		if !toolHomeBridgeName.MatchString(name) || name == "." || name == ".." {
			return nil, fmt.Errorf("invalid toolHomeBridges name %q", name)
		}
	}
	result := make([]string, 0, len(inherited)+1)
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if key != "DYNA_HOME_TOOLS" {
			result = append(result, entry)
		}
	}
	return append(result, "DYNA_HOME_TOOLS="+strings.Join(config.ToolHomeBridges, ",")), nil
}
