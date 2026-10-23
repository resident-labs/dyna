package roomsvc

import (
	"encoding/json"
	"strings"

	"dyna-agent/internal/subagent"
)

// serviceArtifactText is the only structured service-result display conversion.
// Feeds and transcripts share it.
func serviceArtifactText(meta subagent.Meta, result any) (string, bool) {
	if !meta.ServiceContract().Active() || result == nil {
		return "", false
	}
	var data []byte
	if text, ok := result.(string); ok {
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "```json\n") && strings.HasSuffix(text, "```") {
			text = strings.TrimSuffix(strings.TrimPrefix(text, "```json\n"), "```")
		}
		data = []byte(strings.TrimSpace(text))
	} else {
		data, _ = json.Marshal(result)
	}
	if len(data) == 0 || (data[0] != '{' && data[0] != '[') || !json.Valid(data) {
		return "", false
	}
	if meta.Role == "reviewer" {
		return "The review artifact is available on the assigned change.", true
	}
	return "The service artifact is available in its parent thread.", true
}
