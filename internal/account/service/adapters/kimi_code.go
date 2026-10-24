package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// modelsKimiCode reads the managed aliases that `kimi provider list --json`
// projects from config.toml. The provider table can contain API keys, so only
// the non-secret model fields below are decoded and returned.
func modelsKimiCode(ctx context.Context, executable string, env []string) ([]Model, error) {
	out, err := run(ctx, executable, env, "provider", "list", "--json")
	if err != nil {
		return nil, err
	}
	var document struct {
		Models map[string]struct {
			Provider       string   `json:"provider"`
			Model          string   `json:"model"`
			DisplayName    string   `json:"displayName"`
			MaxContextSize int64    `json:"maxContextSize"`
			MaxInputSize   int64    `json:"maxInputSize"`
			MaxOutputSize  int64    `json:"maxOutputSize"`
			Capabilities   []string `json:"capabilities"`
			SupportEfforts []string `json:"supportEfforts"`
			DefaultEffort  string   `json:"defaultEffort"`
		} `json:"models"`
	}
	if json.Unmarshal(out, &document) != nil || len(document.Models) == 0 {
		return nil, errors.New("Kimi Code model catalog is unavailable")
	}
	aliases := make([]string, 0, len(document.Models))
	for alias := range document.Models {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	models := make([]Model, 0, len(aliases))
	for _, alias := range aliases {
		row := document.Models[alias]
		if alias == "" || row.Provider == "" || row.Model == "" {
			continue
		}
		name := row.DisplayName
		if name == "" {
			name = row.Model
		}
		m := Model{ID: alias, Name: name, Provider: row.Provider, Context: row.MaxContextSize, Input: row.MaxInputSize, Output: row.MaxOutputSize, Source: "kimi provider list --json"}
		capabilities := map[string]bool{}
		for _, capability := range row.Capabilities {
			capabilities[capability] = true
		}
		if !strings.Contains(alias, "#") {
			for _, effort := range row.SupportEfforts {
				if effort == "" {
					continue
				}
				m.Efforts = append(m.Efforts, Effort{ID: effort, Model: alias + "#" + effort, Default: effort == row.DefaultEffort})
			}
			if len(row.SupportEfforts) == 0 && capabilities["thinking"] && !capabilities["always_thinking"] {
				m.Efforts = []Effort{{ID: "off", Model: alias + "#off"}, {ID: "on", Model: alias + "#on", Default: true}}
			}
		}
		models = append(models, m)
	}
	if len(models) == 0 {
		return nil, errors.New("Kimi Code model catalog is unavailable")
	}
	return models, nil
}
