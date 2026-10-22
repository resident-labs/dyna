package roomsvc

import (
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/subagent"
)

// serviceArtifactText is the only structured service-result display conversion.
// Feeds and transcripts share it. A keeper result shows as its journal lines.
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
	if meta.Role == subagent.RoleKeeper {
		var result struct {
			Entries []goals.JournalLine `json:"entries"`
		}
		if json.Unmarshal(data, &result) == nil && len(result.Entries) > 0 {
			parts := make([]string, 0, len(result.Entries))
			for _, l := range result.Entries {
				parts = append(parts, fmt.Sprintf("%s · %s\n%s", l.Agent, strings.ReplaceAll(l.Status, "_", " "), l.Summary))
			}
			return strings.Join(parts, "\n\n"), true
		}
		return "Keeper results are in the room's goals journal.", true
	}
	if meta.Role == "reviewer" {
		return "The review artifact is available on the assigned change.", true
	}
	return "The service artifact is available in its parent thread.", true
}
