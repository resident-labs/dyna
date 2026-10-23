package settings

import "fmt"

// loadMerge reads the merge section. It no longer sets anything: the merge
// steward was removed, so a saved merge.stewardProfile is ignored.
//
// TODO: legacy/shim — ignore merge.stewardProfile from settings.json files saved
// before the steward removal; remove this reader when no saved settings carry it.
func (v *Values) loadMerge(raw map[string]any) error {
	section, ok := raw["merge"]
	if !ok {
		return nil
	}
	if _, ok := section.(map[string]any); !ok {
		return fmt.Errorf("merge settings must be an object")
	}
	return nil
}
