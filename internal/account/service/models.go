package service

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
)

// Bundled capabilities supplement exact discovered routes; they never establish account access.
//
//go:embed catalog/opencode.json
var openCodeMetadata []byte

// installationKey names the accounts and executables a catalog belongs to, so
// a provider switch on a harness such as Pi gets a fresh catalog.
func installationKey(installations []Installation) string {
	parts := make([]string, 0, len(installations))
	for _, i := range installations {
		parts = append(parts, i.Executable+"\x00"+i.AccountID)
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x01")
}

// ProfileModels discovers models within the account API request deadline.
func (s *Service) ProfileModels(ctx context.Context, harness string, refresh bool) (Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := runtime.Adapter(harness); err != nil && harness != "mock" {
		return Catalog{}, errors.New("Unknown harness.")
	}
	installations := s.installations()[harness]
	cacheKey := opaque(harness, installationKey(installations))
	if c, ok := s.catalogs[cacheKey]; ok && !refresh && time.Since(c.ObservedAt) < 5*time.Minute {
		return c, nil
	}
	out := Catalog{Models: []Model{}, Compact: profile.CompactRuleFor(harness), Source: "native harness catalog", ObservedAt: time.Now().UTC()}
	if len(installations) == 0 {
		out.Message = "Model discovery is unavailable. Enter an exact model ID or use the harness default."
		return out, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	models, firstErr := []adapters.Model{}, error(nil)
	for _, i := range installations {
		part, err := s.o.Models(probeCtx, harness, i.Executable, i.Env)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		models = append(models, part...)
	}
	if len(models) == 0 && firstErr != nil {
		// Adapter errors carry a safe outcome string, never harness output.
		reason := strings.TrimPrefix(firstErr.Error(), adapters.ErrProbe.Error()+": ")
		out.Message = "The harness model catalog could not be refreshed: " + reason + ". Enter a model ID or retry."
		return out, nil
	}
	var metadata struct {
		Source string                                            `json:"source"`
		Models map[string]struct{ Context, Input, Output int64 } `json:"models"`
	}
	_ = json.Unmarshal(openCodeMetadata, &metadata)
	seen := map[string]bool{}
	for _, m := range models {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		v := Model{ID: m.ID, Label: m.Name, ProviderID: m.Provider, IntegrationID: m.Integration, Context: m.Context, Input: m.Input, Output: m.Output, Category: "other", Source: m.Source, Description: m.Description, Limits: []Limit{}, Efforts: m.Efforts}
		v.Publisher = modelPublisher(m.ID)
		if v.Label == "" {
			v.Label = m.ID
		}
		if v.Source == "" {
			v.Source = out.Source
		}
		if harness == "opencode" {
			if caps, ok := metadata.Models[m.ID]; ok {
				if v.Context == 0 {
					v.Context = caps.Context
				}
				if v.Input == 0 {
					v.Input = caps.Input
				}
				if v.Output == 0 {
					v.Output = caps.Output
				}
				v.Source += "; capabilities: " + metadata.Source
			}
			if strings.HasPrefix(m.ID, "opencode-go/") {
				v.Category = "go"
				v.Limits = goLimits(m.ID)
			}
			if strings.HasPrefix(m.ID, "opencode/") && (strings.HasSuffix(m.ID, "-free") || m.ID == "opencode/big-pickle") {
				v.Category = "free"
				v.Limits = []Limit{{Label: "Free offering", Value: "200 requests per day. The current shared or per-model scope is not published.", Source: "https://opencode.ai/"}}
			}
			switch m.ID {
			case "opencode/glm-5.3-flash-free", "opencode/deepseek-v4.1-flash-free":
				v.Description = "Zero data retention. Prompts and responses are not used for training."
			case "opencode/big-pickle":
				v.Description = "Collected data may be used to improve the model."
			case "opencode/beam-free":
				v.Description = "Data retained for 30 days; not used for training. Regional restrictions apply."
			}
			if v.Description != "" {
				v.Source += "; data policy: https://opencode.ai/v2/docs/console/models"
			}
		}
		out.Models = append(out.Models, v)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Label < out.Models[j].Label })
	if len(out.Models) == 0 {
		out.Message = "The harness returned no models. Enter an exact model ID or use its default."
	}
	s.catalogs[cacheKey] = out
	return out, nil
}

// Recognize unambiguous branded model families without equating routing providers to publishers.
// This display classification never changes routing or account selection.
func modelPublisher(id string) string {
	family := strings.ToLower(id)
	if index := strings.LastIndexByte(family, '/'); index >= 0 {
		family = family[index+1:]
	}
	for prefix, publisher := range map[string]string{"claude-": "Anthropic", "gpt-": "OpenAI", "gemini-": "Google", "grok-": "xAI"} {
		if strings.HasPrefix(family, prefix) {
			return publisher
		}
	}
	return ""
}
func goLimits(id string) []Limit {
	var monthly string
	switch id {
	case "opencode-go/minimax-m3", "opencode-go/glm-5.2":
		monthly = "$60 Go · $180 Go Plus"
	case "opencode-go/kimi-k3":
		monthly = "$15 Go · $60 Go Plus"
	default:
		return []Limit{}
	}
	source := "https://opencode.ai/v2/docs/console/go/"
	return []Limit{
		{Label: "Monthly allowance", Value: monthly, Source: source},
		{Label: "Usage windows", Value: "20% per five hours · 50% per week · 100% per month", Source: source},
		{Label: "Subscription", Value: "Go $10/month · Go Plus $40/month", Source: source},
	}
}
