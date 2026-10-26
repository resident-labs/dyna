package settings

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// KeyAdmissionMaxTurns limits the agent turns that run at once on this
	// machine: auto, off, or a whole number.
	KeyAdmissionMaxTurns = "admission.maxTurns"
	// KeyAdmissionReserve is the memory the automatic limit keeps free: auto, or
	// a size such as 4 GiB.
	KeyAdmissionReserve = "admission.reserve"
	// AdmissionAuto is the value that lets Dyna choose from free memory and load.
	AdmissionAuto = "auto"
	// AdmissionOff turns the limit off.
	AdmissionOff = "off"
)

func (v *Values) loadAdmission(raw map[string]any) error {
	section, ok := raw["admission"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("admission settings must be an object")
	}
	for key, name := range map[string]string{KeyAdmissionMaxTurns: "maxTurns", KeyAdmissionReserve: "reserve"} {
		if value, ok := values[name]; ok {
			if err := v.setAdmission(key, numberText(value)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *Values) setAdmission(key, value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	switch key {
	case KeyAdmissionMaxTurns:
		if value != AdmissionAuto && value != AdmissionOff {
			if n, err := strconv.Atoi(value); err != nil || n < 1 {
				return fmt.Errorf("%s must be auto, off, or a whole number of turns above zero", key)
			}
		}
		v.AdmissionMaxTurns = value
	case KeyAdmissionReserve:
		if value == AdmissionAuto || value == "" {
			v.AdmissionReserve = 0
			break
		}
		n, err := ParseBytes(value)
		if err != nil || n < 1 {
			return fmt.Errorf("%s must be auto or a size such as 4 GiB", key)
		}
		v.AdmissionReserve = n
	}
	v.fromFile[key] = true
	return nil
}

func (v Values) saveAdmission(raw map[string]any) {
	values := nested(raw, "admission")
	values["maxTurns"] = v.AdmissionMaxTurns
	reserve := AdmissionAuto
	if v.AdmissionReserve > 0 {
		reserve = FormatBytes(v.AdmissionReserve)
	}
	values["reserve"] = reserve
	raw["admission"] = values
}
