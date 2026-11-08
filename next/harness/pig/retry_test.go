package pig

import (
	"encoding/json"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding"
)

func TestModelRetryTotalBudget(t *testing.T) {
	for _, input := range []string{`{}`, `{"retry":{"maxRetries":100000,"baseDelayMs":600000,"maxAgentDelayMs":600000,"provider":{"maxRetries":10000,"maxRetryDelayMs":0}}}`, `{"retry":{"enabled":false,"maxRetries":0,"baseDelayMs":0,"maxAgentDelayMs":0,"provider":{"maxRetries":0,"maxRetryDelayMs":1}}}`} {
		var settings coding.Settings
		if err := json.Unmarshal([]byte(input), &settings); err != nil {
			t.Fatal(err)
		}
		manager := coding.NewInMemorySettingsManager(settings)
		if err := boundModelRetries(manager); err != nil {
			t.Fatal(err)
		}
		a, p := manager.GetRetrySettings(), manager.GetProviderRetrySettings()
		if a.MaxRetries > 3 || *p.MaxRetries > 1 || p.MaxRetryDelayMs > 20000 {
			t.Fatalf("unbounded settings: %+v %+v", a, p)
		}
		total := 0
		for attempt := 1; attempt <= a.MaxRetries; attempt++ {
			total += ai.RetryDelayMs(ai.RetryPolicy{BaseDelayMs: a.BaseDelayMs, MaxAgentDelayMs: &a.MaxAgentDelayMs}, attempt)
		}
		total += (a.MaxRetries + 1) * *p.MaxRetries * p.MaxRetryDelayMs
		if total > 90000 {
			t.Fatalf("hidden retry backoff %d ms", total)
		}
	}
}
