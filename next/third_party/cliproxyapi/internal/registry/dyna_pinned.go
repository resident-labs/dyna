package registry

import "encoding/json"

// DynaPinnedPlanModels returns only the immutable catalog embedded in this build.
// Codex tiers are returned separately so consumers can choose their policy.
type DynaPinnedPlanModels struct {
	Claude, Kimi, XAI, CodexFree, CodexTeam, CodexPlus, CodexPro []string
}

func DynaPinnedModels() (DynaPinnedPlanModels, error) {
	var catalog staticModelsJSON
	if err := json.Unmarshal(embeddedModelsJSON, &catalog); err != nil {
		return DynaPinnedPlanModels{}, err
	}
	ids := func(models []*ModelInfo) []string {
		out := make([]string, 0, len(models))
		for _, model := range models {
			out = append(out, model.ID)
		}
		return out
	}
	return DynaPinnedPlanModels{Claude: ids(catalog.Claude), Kimi: ids(catalog.Kimi), XAI: ids(catalog.XAI), CodexFree: ids(catalog.CodexFree), CodexTeam: ids(catalog.CodexTeam), CodexPlus: ids(catalog.CodexPlus), CodexPro: ids(catalog.CodexPro)}, nil
}
