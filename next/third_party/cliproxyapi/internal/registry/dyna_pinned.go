package registry

import "encoding/json"

// DynaPinnedPlanModels returns only the immutable catalog embedded in this build.
// The Codex list is shared by all plan tiers, so one activation works for each.
type DynaPinnedPlanModels struct {
	Claude, Codex, Kimi, XAI []string
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
	sets := []map[string]bool{}
	for _, models := range [][]*ModelInfo{catalog.CodexFree, catalog.CodexTeam, catalog.CodexPlus, catalog.CodexPro} {
		set := make(map[string]bool, len(models))
		for _, model := range models {
			set[model.ID] = true
		}
		sets = append(sets, set)
	}
	var codex []string
	for _, id := range ids(catalog.CodexPro) {
		if sets[0][id] && sets[1][id] && sets[2][id] {
			codex = append(codex, id)
		}
	}
	return DynaPinnedPlanModels{Claude: ids(catalog.Claude), Codex: codex, Kimi: ids(catalog.Kimi), XAI: ids(catalog.XAI)}, nil
}
