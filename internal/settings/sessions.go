package settings

import (
	"fmt"
	"time"
)

const KeySessionCheckInInterval = "sessions.checkInInterval"

func (v *Values) loadSessions(raw map[string]any) error {
	section, ok := raw["sessions"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("sessions settings must be an object")
	}
	if value, ok := values["checkInInterval"]; ok {
		return v.setSession(KeySessionCheckInInterval, fmt.Sprint(value))
	}
	return nil
}

func (v *Values) setSession(key, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return fmt.Errorf("%s must be a non-negative duration", key)
	}
	v.SessionCheckInInterval = d
	v.fromFile[key] = true
	return nil
}

func (v Values) saveSessions(raw map[string]any) {
	values := nested(raw, "sessions")
	values["checkInInterval"] = v.SessionCheckInInterval.String()
	raw["sessions"] = values
	// Removed keeper settings are no longer written.
	if goals, ok := raw["goals"].(map[string]any); ok {
		delete(goals, "keeperTimeout")
		delete(goals, "keeperProfile")
	}
}
