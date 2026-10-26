package settings

import (
	"fmt"
	"strings"
)

// KeyDoctorCondition is the condition under which the doctor hook examines a
// machine. Empty uses the doctor recipe's default.
const KeyDoctorCondition = "doctor.condition"

func (v *Values) loadDoctor(raw map[string]any) error {
	section, ok := raw["doctor"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("doctor settings must be an object")
	}
	if value, ok := values["condition"]; ok {
		return v.setDoctor(KeyDoctorCondition, fmt.Sprint(value))
	}
	return nil
}

func (v *Values) setDoctor(key, value string) error {
	// The setting holds text only. configsvc checks it against the doctor's
	// grammar, so the settings package stays out of the computer daemon's imports.
	v.DoctorCondition = strings.TrimSpace(value)
	v.fromFile[key] = true
	return nil
}

func (v Values) saveDoctor(raw map[string]any) {
	values := nested(raw, "doctor")
	values["condition"] = v.DoctorCondition
	raw["doctor"] = values
}
