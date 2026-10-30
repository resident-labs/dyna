package contract

import "encoding/json"

// DecodeEventBody decodes a stored registered event body.
func DecodeEventBody(kind EventType, body []byte) (EventBody, error) {
	return decodeEventBody(kind, json.RawMessage(body))
}
