package policy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// exactJSON closes the gap between Go's case-insensitive field matching and
// Discord's case-sensitive JSON. It also refuses duplicate keys at every depth.
func exactJSON(decoder *json.Decoder, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	raw := target == reflect.TypeOf(json.RawMessage{})
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		fields := map[string]reflect.Type{}
		if target.Kind() == reflect.Struct {
			for i := 0; i < target.NumField(); i++ {
				fieldInfo := target.Field(i)
				name, _, _ := strings.Cut(fieldInfo.Tag.Get("json"), ",")
				if name != "" && name != "-" {
					fields[name] = fieldInfo.Type
				}
			}
		}
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key := keyToken.(string)
			field, ok := fields[key]
			if raw {
				field = target
				ok = true
			}
			if !ok {
				return fmt.Errorf("unknown field %q", key)
			}
			if seen[key] {
				return fmt.Errorf("duplicate field %q", key)
			}
			seen[key] = true
			if err := exactJSON(decoder, field); err != nil {
				return err
			}
		}
	case '[':
		if !raw && target.Kind() != reflect.Slice {
			return fmt.Errorf("unexpected array")
		}
		for decoder.More() {
			element := target
			if !raw {
				element = target.Elem()
			}
			if err := exactJSON(decoder, element); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
