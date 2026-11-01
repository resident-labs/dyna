package registry

import "encoding/json"

// DynaPinnedPlanModels returns only the immutable catalog embedded in this build.
// Codex contains the union of tiers; CodexCommon contains their intersection.
type DynaPinnedPlanModels struct {
	Claude, Codex, CodexCommon, Kimi, XAI []string
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
	var codex, common []string
	seen := make(map[string]bool)
	for _, tier := range [][]*ModelInfo{catalog.CodexFree, catalog.CodexTeam, catalog.CodexPlus, catalog.CodexPro} {
		for _, id := range ids(tier) {
			if !seen[id] {
				codex = append(codex, id)
				seen[id] = true
			}
		}
	}
	for _, id := range codex {
		if sets[0][id] && sets[1][id] && sets[2][id] && sets[3][id] {
			common = append(common, id)
		}
	}
	return DynaPinnedPlanModels{Claude: ids(catalog.Claude), Codex: codex, CodexCommon: common, Kimi: ids(catalog.Kimi), XAI: ids(catalog.XAI)}, nil
}
