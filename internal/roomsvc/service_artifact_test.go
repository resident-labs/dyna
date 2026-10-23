package roomsvc

import (
	"strings"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestOtherStructuredServiceArtifactsUsePointers(t *testing.T) {
	for _, role := range []string{"reviewer", ""} {
		meta := subagent.Meta{Service: true, Role: role}
		for _, result := range []any{map[string]any{"private": "value"}, `{"private":"value"}`, "```json\n{\"private\":\"value\"}\n```"} {
			text, ok := serviceArtifactText(meta, result)
			if !ok || strings.Contains(text, "private") || strings.Contains(text, "{") {
				t.Fatalf("raw service output: %q %v", text, ok)
			}
		}
	}
}
