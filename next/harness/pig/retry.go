package pig

import (
	"encoding/json"
	"fmt"

	"github.com/MichaelKinsy/PiG/coding"
)

// boundModelRetries uses PiG's existing retry loops, not another retry layer.
// Agent waits total at most 2 + 4 + 4 seconds. Each of its four requests
// can wait at most 20 seconds for one provider retry: 90 seconds in all.
// Zero/disabled and stricter user settings remain stricter.
func boundModelRetries(settings *coding.SettingsManager) error {
	agent := settings.GetRetrySettings()
	provider := settings.GetProviderRetrySettings()
	retries := 0
	if provider.MaxRetries != nil {
		retries = min(1, max(0, *provider.MaxRetries))
	}
	delay := provider.MaxRetryDelayMs
	if delay <= 0 {
		delay = 20000
	}
	// PiG exposes Settings but not its nested retry construction types.
	// Decode this host-owned override once through its public settings boundary.
	var override coding.Settings
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"retry":{"enabled":%t,"maxRetries":%d,"baseDelayMs":%d,"maxAgentDelayMs":%d,"provider":{"maxRetries":%d,"maxRetryDelayMs":%d}}}`,
		agent.Enabled, min(3, max(0, agent.MaxRetries)), min(2000, max(0, agent.BaseDelayMs)), min(4000, max(0, agent.MaxAgentDelayMs)), retries, min(20000, delay))), &override); err != nil {
		return err
	}
	settings.ApplyOverrides(override)
	return nil
}
