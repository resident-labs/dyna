package settings

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// KeyAdmissionMode is off, observe or enforce. Observe records the turns
	// that enforcing would have held and holds none.
	KeyAdmissionMode = "admission.mode"
	// KeyAdmissionMaxWait bounds how long an enforced turn waits.
	KeyAdmissionMaxWait = "admission.maxWait"
	// KeyAdmissionMaxTurns limits the agent turns that run at once on this
	// machine: auto, or a whole number.
	KeyAdmissionMaxTurns = "admission.maxTurns"
	// KeyAdmissionReserve is the memory the automatic limit keeps free: auto, or
	// a size such as 4 GiB.
	KeyAdmissionReserve = "admission.reserve"
	// AdmissionAuto is the value that lets Dyna choose from free memory and load.
	AdmissionAuto = "auto"
	// The values of admission.mode.
	AdmissionOff     AdmissionMode = "off"
	AdmissionObserve AdmissionMode = "observe"
	AdmissionEnforce AdmissionMode = "enforce"
)

// AdmissionMode is what the turn limit does: nothing, record, or hold.
type AdmissionMode string

// admissionFile is the admission section of settings.json. A number or a
// string both read as text, so 12 and "12" are the same limit.
type admissionFile struct {
	Mode     AdmissionMode `json:"mode"`
	MaxWait  flexText      `json:"maxWait"`
	MaxTurns flexText      `json:"maxTurns"`
	Reserve  flexText      `json:"reserve"`
}

type flexText string

func (t *flexText) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*t = flexText(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("must be text or a number")
	}
	*t = flexText(n.String())
	return nil
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
	for _, f := range []struct{ key, value string }{
		{KeyAdmissionMode, string(file.Mode)}, {KeyAdmissionMaxWait, string(file.MaxWait)},
		{KeyAdmissionMaxTurns, string(file.MaxTurns)}, {KeyAdmissionReserve, string(file.Reserve)},
	} {
		if f.value == "" {
			continue
		}
		if err := v.setAdmission(f.key, f.value); err != nil {
			return err
		}
	}
	return nil
}

func (v *Values) setAdmission(key, value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	switch key {
	case KeyAdmissionMode:
		mode := AdmissionMode(value)
		if mode != AdmissionOff && mode != AdmissionObserve && mode != AdmissionEnforce {
			return fmt.Errorf("%s must be off, observe or enforce", key)
		}
		v.AdmissionMode = mode
	case KeyAdmissionMaxWait:
		d, err := time.ParseDuration(value)
		if err != nil || d < time.Second {
			return fmt.Errorf("%s must be a duration of at least one second, such as 2m", key)
		}
		v.AdmissionMaxWait = d
	case KeyAdmissionMaxTurns:
		if value != AdmissionAuto {
			if n, err := strconv.Atoi(value); err != nil || n < 1 {
				return fmt.Errorf("%s must be auto, or a whole number of turns above zero", key)
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
	values["mode"] = v.AdmissionMode
	values["maxWait"] = v.AdmissionMaxWait.String()
	values["maxTurns"] = v.AdmissionMaxTurns
	reserve := AdmissionAuto
	if v.AdmissionReserve > 0 {
		reserve = FormatBytes(v.AdmissionReserve)
	}
	values["reserve"] = reserve
	raw["admission"] = values
}
