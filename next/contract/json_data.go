package contract

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// JSONData carries JSON text as a string on the internal wire. The owning post boundary validates and
// canonicalizes its contents. Ordinary API JSON decoding cannot round its numbers or rewrite token spellings.
// Empty means absent; JSON null is the four-character text "null". The workflow JS API still takes a JSON value.
type JSONData string

func (d *JSONData) UnmarshalJSON(wire []byte) error {
	if !utf8.Valid(wire) {
		return errors.New("encoded JSON data is not valid UTF-8")
	}
	var text *string
	if err := json.Unmarshal(wire, &text); err != nil {
		return err
	}
	if text == nil {
		return errors.New("data is JSON text encoded as a string; use \"null\" for JSON null")
	}
	*d = JSONData(*text)
	return nil
}
