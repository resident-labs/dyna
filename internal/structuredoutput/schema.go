// Package structuredoutput validates JSON responses and defines correction policy.
package structuredoutput

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// SchemaSuffix is the JSON-only response contract appended to a prompt whose
// answer must validate against schemaJSON.
func SchemaSuffix(schemaJSON string) string {
	return "\n\n---\nOUTPUT FORMAT: Respond with ONLY a JSON value that validates against this JSON Schema. No prose, no markdown fences, no explanation:\n" + schemaJSON
}

// CheckSchema reports whether schemaJSON compiles as a JSON Schema.
func CheckSchema(schemaJSON string) error {
	_, err := Compile(schemaJSON)
	return err
}

// Schema validates each response against one compiled schema.
type Schema struct{ schema *jsonschema.Schema }

func Compile(schemaJSON string) (*Schema, error) {
	schema, err := jsonschema.CompileString("schema.json", schemaJSON)
	if err != nil {
		return nil, fmt.Errorf("invalid schema: %w", err)
	}
	return &Schema{schema: schema}, nil
}

func (s *Schema) Validate(raw string) (any, error) {
	val, err := extractJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := s.schema.Validate(val); err != nil {
		return nil, err
	}
	return val, nil
}

// SchemaCorrection is the follow-up text asking a worker to fix an invalid
// response; includePrevious repeats a truncated copy of it for fresh sessions.
func SchemaCorrection(cause error, raw string, includePrevious bool) string {
	text := "Your previous response was invalid: " + truncate(cause.Error(), 800)
	if includePrevious {
		text += "\nPrevious response (truncated): " + truncate(raw, 1500)
	}
	return text + "\nRespond again with ONLY the corrected JSON."
}

// SchemaAttempts is how many responses a schema call may take (one plus two
// corrections).
const SchemaAttempts = 3

// extractJSON finds the first JSON value in model output, tolerating prose
// and markdown fences around it.
func extractJSON(s string) (any, error) {
	s = strings.TrimSpace(s)
	// A response that is already a JSON value wins: markdown fences inside its
	// strings (e.g. code examples in a report field) must not be re-extracted.
	if v, ok := decodeWholeJSON(s); ok {
		return v, nil
	}
	// Otherwise prefer fenced blocks over prose.
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		if v, ok := decodeWholeJSON(strings.TrimSpace(m[1])); ok {
			return v, nil
		}
	}
	return decodeJSONFrom(s)
}

func decodeWholeJSON(s string) (any, bool) {
	if !json.Valid([]byte(s)) {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, false
	}
	return v, true
}

func decodeJSONFrom(s string) (any, error) {
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return nil, fmt.Errorf("no JSON object/array found in output")
	}
	dec := json.NewDecoder(strings.NewReader(s[start:]))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}
	return v, nil
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
