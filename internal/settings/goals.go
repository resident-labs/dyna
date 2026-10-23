package settings

import (
	"fmt"
	"strings"
	"time"
)

const (
	KeyGoalKeeperTimeout = "goals.keeperTimeout"
	KeyGoalKeeperProfile = "goals.keeperProfile"
	// KeyGoalEnabled is the goal keeper's on/off switch. Off stops nudges of
	// stale goals and queued keeper notices.
	KeyGoalEnabled = "goals.enabled"
)

// loadGoals reads the known goal keys. Keys of the old per-goal keeper
// (keeperInterval, nudgesPerGoal, nudgesPerRoom) are ignored.
func (v *Values) loadGoals(raw map[string]any) error {
	section, ok := raw["goals"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("goals settings must be an object")
	}
	for _, key := range []string{KeyGoalKeeperTimeout, KeyGoalKeeperProfile, KeyGoalEnabled} {
		if value, ok := values[strings.TrimPrefix(key, "goals.")]; ok {
			if err := v.setGoal(key, fmt.Sprint(value)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (v *Values) setGoal(key, value string) error {
	if key == KeyGoalEnabled {
		on, err := parseBool(value)
		if err != nil {
			return err
		}
		v.GoalKeeperEnabled = on
		v.fromFile[key] = true
		return nil
	}
	durations := map[string]*time.Duration{KeyGoalKeeperTimeout: &v.GoalKeeperTimeout}
	if field := durations[key]; field != nil {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s must be a positive duration", key)
		}
		*field = d
	} else {
		v.GoalKeeperProfile = strings.TrimSpace(value)
	}
	v.fromFile[key] = true
	return nil
}
func (v Values) saveGoals(raw map[string]any) {
	values := nested(raw, "goals")
	values["keeperProfile"], values["keeperTimeout"] = v.GoalKeeperProfile, v.GoalKeeperTimeout.String()
	values["enabled"] = v.GoalKeeperEnabled
	raw["goals"] = values
}
