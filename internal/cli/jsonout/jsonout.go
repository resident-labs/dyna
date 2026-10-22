// Package jsonout prints a command result as indented JSON.
package jsonout

import (
	"encoding/json"
	"io"
)

// Write prints v as two-space indented JSON with a trailing newline.
func Write(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
