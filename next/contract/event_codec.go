package contract

import "encoding/json"

// DecodeEventBody decodes a stored registered event body.
func DecodeEventBody(kind EventType, body []byte) (EventBody, error) {
	return decodeEventBody(kind, json.RawMessage(body))
}

// EventTypes lists every event type.
func EventTypes() []EventType { return eventBodyTypes }

// EventDoc says in one line what an event is, from its type's doc comment.
func EventDoc(kind EventType) string { return eventDocs[kind] }
