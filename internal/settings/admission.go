package settings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// KeyAdmissionMode is off or observe. Observe records when a limit on
// concurrent agent turns would have held new turns, and holds nothing.
const KeyAdmissionMode = "admission.mode"

// AdmissionMode is what the admission watcher does.
type AdmissionMode string

// The values of admission.mode.
const (
	AdmissionOff     AdmissionMode = "off"
	AdmissionObserve AdmissionMode = "observe"
)

// admissionFile is the admission section of settings.json.
type admissionFile struct {
	Mode AdmissionMode `json:"mode"`
}

func (v *Values) loadAdmission(raw map[string]any) error {
	section, ok := raw["admission"]
	if !ok {
		return nil
	}
	if _, isObject := section.(map[string]any); !isObject {
		return fmt.Errorf("admission settings must be an object")
	}
	data, err := json.Marshal(section)
	if err != nil {
		return err
	}
	var file admissionFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("admission settings: %w", err)
	}
	if file.Mode == "" {
		return nil
	}
	return v.setAdmission(KeyAdmissionMode, string(file.Mode))
}

func (v *Values) setAdmission(key, value string) error {
	mode := AdmissionMode(strings.ToLower(strings.TrimSpace(value)))
	if mode != AdmissionOff && mode != AdmissionObserve {
		return fmt.Errorf("%s must be off or observe", key)
	}
	v.AdmissionMode = mode
	v.fromFile[key] = true
	return nil
}

func (v Values) saveAdmission(raw map[string]any) {
	values := nested(raw, "admission")
	values["mode"] = v.AdmissionMode
	raw["admission"] = values
}
