package adapters

import (
	"context"
	"encoding/json"
	"errors"
)

// piListedModel is one entry of `pi --list-models --json` (fork level 2).
type piListedModel struct {
	Provider       string   `json:"provider"`
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ContextWindow  int64    `json:"contextWindow"`
	MaxTokens      int64    `json:"maxTokens"`
	Reasoning      bool     `json:"reasoning"`
	ThinkingLevels []string `json:"thinkingLevels"`
}

// modelsPi decodes the JSON array that `pi --list-models --json` prints. The
// exact token windows and per-model thinking levels come from Pi's catalog.
func modelsPi(ctx context.Context, executable string, env []string) ([]Model, error) {
	out, err := run(ctx, executable, env, "--list-models", "--json")
	if err != nil {
		return nil, err
	}
	var catalog []piListedModel
	if json.Unmarshal(out, &catalog) != nil || len(catalog) == 0 {
		return nil, errors.New("Pi model catalog is unavailable")
	}
	models := make([]Model, 0, len(catalog))
	for _, listed := range catalog {
		m := Model{
			ID:       listed.Provider + "/" + listed.ID,
			Name:     listed.Name,
			Provider: listed.Provider,
			Context:  listed.ContextWindow,
			Output:   listed.MaxTokens,
			Source:   "pi --list-models",
		}
		for _, level := range listed.ThinkingLevels {
			m.Efforts = append(m.Efforts, Effort{ID: level, Args: []string{"--thinking", level}})
		}
		models = append(models, m)
	}
	return models, nil
}
