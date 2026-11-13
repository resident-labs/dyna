package policy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// exactJSON closes the gap between Go's case-insensitive field matching and
// Discord's case-sensitive JSON. It also refuses duplicate keys at every depth.
func exactJSON(d *json.Decoder, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, e := d.Token()
	if e != nil {
		return e
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		fields := map[string]reflect.Type{}
		if t.Kind() == reflect.Struct {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if name != "" && name != "-" {
					fields[name] = f.Type
				}
			}
		}
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			key := k.(string)
			field, ok := fields[key]
			if !ok {
				return fmt.Errorf("unknown field %q", key)
			}
			if seen[key] {
				return fmt.Errorf("duplicate field %q", key)
			}
			seen[key] = true
			if e := exactJSON(d, field); e != nil {
				return e
			}
		}
	case '[':
		if t.Kind() != reflect.Slice {
			return fmt.Errorf("unexpected array")
		}
		for d.More() {
			if e := exactJSON(d, t.Elem()); e != nil {
				return e
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, e = d.Token()
	return e
}
