package settings

import "fmt"

const KeyMergeStewardProfile = "merge.stewardProfile"

// StewardProfileOff turns the merge steward off: a failed batch skips the
// steward and goes straight to the existing root notice.
const StewardProfileOff = "off"

func (v *Values) loadMerge(raw map[string]any) error {
	section, ok := raw["merge"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("merge settings must be an object")
	}
	if value, ok := values["stewardProfile"]; ok {
		return v.Set(KeyMergeStewardProfile, fmt.Sprint(value))
	}
	return nil
}
