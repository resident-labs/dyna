package adapters

import (
	"encoding/json"
	"strings"
)

// decodeProjection handles the native location-scoped {location,data} envelope
// and historical direct arrays. It rejects null rather than reporting success.
func decodeProjection(data []byte, out any) error {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 || string(data) == "null" {
		return ErrProbe
	}
	if data[0] == '{' {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
			return ErrProbe
		}
		return json.Unmarshal(envelope.Data, out)
	}
	return json.Unmarshal(data, out)
}
