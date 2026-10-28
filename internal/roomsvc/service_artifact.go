package roomsvc

import (
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/turntext"
)

// serviceArtifactText is the only structured service-result display conversion.
// Feeds and transcripts share it.
func serviceArtifactText(meta subagent.Meta, result any) (string, bool) {
	return turntext.ServiceArtifactText(meta, result)
}
