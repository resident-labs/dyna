package interactive

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

func TestGuideIndexRendersAuthorityRules(t *testing.T) {
	out, err := guideOutput(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, authority.Guide()) {
		t.Fatal("guide index must include the generated authority section")
	}
	for _, rule := range authority.Rules {
		if !strings.Contains(out, rule.Action) {
			t.Errorf("guide index misses authority action %q", rule.Action)
		}
	}
}
