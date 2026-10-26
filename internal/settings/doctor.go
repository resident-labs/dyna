package settings

import (
	"fmt"
	"strings"

	"dyna-agent/internal/doctor"
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
	value = strings.TrimSpace(value)
	if value != "" {
		if _, err := doctor.ParseCondition(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	v.DoctorCondition = value
	v.fromFile[key] = true
	return nil
}

func (v Values) saveDoctor(raw map[string]any) {
	values := nested(raw, "doctor")
	values["condition"] = v.DoctorCondition
	raw["doctor"] = values
}
