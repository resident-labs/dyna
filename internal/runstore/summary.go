package runstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// summaryRunes bounds a summary line.
const summaryRunes = 200

// Summary is one readable line for a worker or run result, taken from the
// result's own fields: summary, title, name or description; a count and the
// first record for a list of records; else the first string field in the
// author's order, else the first scalar. Text that is not JSON gives its
// first line.
func Summary(raw string) string {
	raw = strings.TrimSpace(raw)
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		line, _, _ := strings.Cut(raw, "\n")
		return clip(line)
	}
	switch x := v.(type) {
	case string:
		line, _, _ := strings.Cut(strings.TrimSpace(x), "\n")
		return clip(line)
	case []any:
		return listLine(len(x), "items", x)
	case map[string]any:
		for _, k := range []string{"summary", "title", "name", "description"} {
			if s, ok := x[k].(string); ok && strings.TrimSpace(s) != "" {
				return clip(s)
			}
		}
		fields := orderedFields(raw)
		for _, f := range fields {
			if list, ok := x[f].([]any); ok && len(list) > 0 {
				if _, isObject := list[0].(map[string]any); isObject {
					return listLine(len(list), f, list)
				}
			}
		}
		for _, f := range fields {
			if s, ok := x[f].(string); ok && strings.TrimSpace(s) != "" {
				return clip(s)
			}
		}
		for _, f := range fields {
			switch x[f].(type) {
			case map[string]any, []any, nil:
			default:
				return clip(fmt.Sprint(x[f]))
			}
		}
		return ""
	case nil:
		return ""
	default:
		return clip(fmt.Sprint(x))
	}
}

func listLine(n int, noun string, list []any) string {
	first := ""
	if len(list) > 0 {
		if b, err := json.Marshal(list[0]); err == nil {
			first = Summary(string(b))
		}
	}
	if first == "" {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return clip(fmt.Sprintf("%d %s: %s", n, noun, first))
}

// orderedFields lists a JSON object's top-level keys in source order.
func orderedFields(raw string) []string {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		key, _ := tok.(string)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
		keys = append(keys, key)
	}
	return keys
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > summaryRunes {
		return string(r[:summaryRunes]) + "…"
	}
	return s
}
