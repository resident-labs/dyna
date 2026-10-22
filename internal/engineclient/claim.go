package engineclient

import (
	"encoding/json"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/mode"
)

// TODO: legacy/shim remove later — sends the CLI session and room for the old report-only guards, never as proof of identity; remove with caller enforcement (g3419).
func processClaim() string {
	claim := callspec.Claim{}
	if current, ok := mode.CurrentSession(); ok {
		claim.Session = current.ID
	}
	if stamp, ok := mode.StampFromEnv(); ok {
		claim.Room = stamp.RoomID
		if claim.Room == "" {
			claim.Room = stamp.Room
		}
	}
	raw, _ := json.Marshal(claim)
	return string(raw)
}
