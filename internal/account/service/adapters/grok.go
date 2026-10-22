package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// modelsGrok lists the IDs that `grok models` prints for the signed-in
// account. That command also refreshes $GROK_HOME/models_cache.json, which
// adds names, context windows and reasoning efforts (Grok 1.0.62).
func modelsGrok(ctx context.Context, executable string, env []string) ([]Model, error) {
	out, err := run(ctx, executable, env, "--no-leader", "models")
	if err != nil {
		return nil, err
	}
	cache := grokModelCache(env)
	var models []Model
	seen := map[string]bool{}
	listed := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "Available models:" {
			listed = true
			continue
		}
		bullet, rest, ok := strings.Cut(line, " ")
		if !listed || !ok || bullet != "*" && bullet != "-" {
			continue
		}
		id, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		m := Model{ID: id, Name: id, Provider: "xai", Source: "grok models"}
		if info, ok := cache[id]; ok && !info.Hidden {
			if info.Name != "" {
				m.Name = info.Name
			}
			m.Description, m.Context = info.Description, info.Context
			for _, e := range info.Efforts {
				if e.Value != "" {
					m.Efforts = append(m.Efforts, Effort{ID: e.Value, Label: e.Label, Default: e.Default, Args: []string{"--reasoning-effort", e.Value}})
				}
			}
		}
		models = append(models, m)
	}
	if len(models) == 0 {
		return nil, errors.New("Grok model catalog is unavailable")
	}
	return models, nil
}

type grokCachedModel struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Context     int64  `json:"context_window"`
	Hidden      bool   `json:"hidden"`
	Efforts     []struct {
		Value   string `json:"value"`
		Label   string `json:"label"`
		Default bool   `json:"default"`
	} `json:"reasoning_efforts"`
}

// grokModelCache is optional metadata. A missing or changed cache leaves the
// listed IDs without details.
func grokModelCache(env []string) map[string]grokCachedModel {
	home := ""
	if env == nil {
		home = os.Getenv("GROK_HOME")
	}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GROK_HOME="); ok {
			home = v
		}
	}
	out := map[string]grokCachedModel{}
	if home == "" {
		return out
	}
	raw, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
	if err != nil {
		return out
	}
	var cache struct {
		Models map[string]struct {
			Info grokCachedModel `json:"info"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &cache) != nil {
		return out
	}
	for id, m := range cache.Models {
		out[id] = m.Info
	}
	return out
}
