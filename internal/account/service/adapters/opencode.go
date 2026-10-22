package adapters

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
)

func modelsOpenCode(ctx context.Context, executable string, env []string) ([]Model, error) {
	// v2 metadata uses the same native server resolution as auth discovery.
	// Do not gate on semver major: shipped v2 prereleases can report v0.0.0-v2.
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return nil, ErrProbe
	}
	b, err := run(ctx, executable, env, "api", "GET", "/api/model", "--param", "location[directory]="+cwd)
	if err == nil {
		var rows []struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Name       string `json:"name"`
			Enabled    *bool  `json:"enabled"`
			// provider/model#variant selects a variant (opencode run --help).
			Variants []struct {
				ID string `json:"id"`
			} `json:"variants"`
			Limit struct {
				Context int64 `json:"context"`
				Input   int64 `json:"input"`
				Output  int64 `json:"output"`
			} `json:"limit"`
		}
		if decodeProjection(b, &rows) == nil && strings.TrimSpace(string(b)) != "null" {
			providers := map[string]string{}
			if raw, e := run(ctx, executable, env, "api", "GET", "/api/provider", "--param", "location[directory]="+cwd); e == nil {
				var ps []struct {
					ID          string `json:"id"`
					Integration string `json:"integrationID"`
				}
				if decodeProjection(raw, &ps) == nil {
					for _, p := range ps {
						providers[p.ID] = p.Integration
					}
				}
			}
			var models []Model
			seen := map[string]bool{}
			for _, r := range rows {
				if r.ID == "" || r.ProviderID == "" || (r.Enabled != nil && !*r.Enabled) {
					continue
				}
				id := r.ProviderID + "/" + r.ID
				if seen[id] {
					continue
				}
				seen[id] = true
				m := Model{ID: id, Name: r.Name, Provider: r.ProviderID, Integration: providers[r.ProviderID], Context: r.Limit.Context, Input: r.Limit.Input, Output: r.Limit.Output, Source: "opencode api GET /api/model; /api/provider"}
				for _, v := range r.Variants {
					if v.ID != "" {
						m.Efforts = append(m.Efforts, Effort{ID: v.ID, Model: id + "#" + v.ID})
					}
				}
				models = append(models, m)
			}
			if len(models) > 0 {
				return models, nil
			}
		}
	}
	// v1 and early v2 can still enumerate routes, without inferred integration
	// routing or publisher limits. No curated/static fallback is discovery.
	b, err = run(ctx, executable, env, "models")
	if err != nil {
		return nil, err
	}
	var models []Model
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		id := strings.TrimSpace(line)
		if !routeID.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		provider, _, _ := strings.Cut(id, "/")
		models = append(models, Model{ID: id, Name: id, Provider: provider, Source: "opencode models"})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if len(models) == 0 {
		return nil, ErrUnsupported
	}
	return models, nil
}

var routeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]*/[A-Za-z0-9][A-Za-z0-9._:/@-]*$`)
