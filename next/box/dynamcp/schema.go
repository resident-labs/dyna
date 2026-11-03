// Package dynamcp exposes the registered agent actions through MCP.
// Schema normalization follows T3 Code's advertisedJsonSchema.ts at 15fe283 (MIT).
package dynamcp

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
)

type schema struct {
	Type        string            `json:"type,omitempty"`
	Properties  map[string]schema `json:"properties,omitempty"`
	Items       *schema           `json:"items,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
	Required    []string          `json:"required,omitempty"`
	Description string            `json:"description,omitempty"`
}

type action struct {
	method   contract.Method
	op       contract.OpKind
	request  reflect.Type
	local    tools.BoxHandler
	help     string
	readOnly bool
}

type annotations struct {
	ReadOnly    bool `json:"readOnlyHint"`
	Destructive bool `json:"destructiveHint"`
	Idempotent  bool `json:"idempotentHint"`
	OpenWorld   bool `json:"openWorldHint"`
}
type tool struct {
	Name        string      `json:"name"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	InputSchema schema      `json:"inputSchema"`
	Annotations annotations `json:"annotations"`
}

func typeSchema(t reflect.Type) schema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeFor[time.Time]() {
		return schema{Type: "string"}
	}
	switch t.Kind() {
	case reflect.Bool:
		return schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return schema{Type: "number"}
	case reflect.String:
		return schema{Type: "string"}
	case reflect.Slice, reflect.Array:
		item := typeSchema(t.Elem())
		return schema{Type: "array", Items: &item}
	case reflect.Struct:
		props := map[string]schema{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			prop := typeSchema(f.Type)
			prop.Description = f.Tag.Get("desc")
			props[name] = prop
		}
		return schema{Type: "object", Properties: props}
	default:
		return schema{Type: "object"}
	}
}

// requestProperties lists the fields an action takes. An operation also takes the replay key.
func requestProperties(a action) map[string]schema {
	props := typeSchema(a.request).Properties
	if a.op != "" {
		props["clientId"] = schema{Type: "string"}
	}
	return props
}

func registry() ([]tool, map[string]map[string]action, error) {
	actions := map[string]map[string]action{}
	add := func(name, verb string, a action) {
		if actions[name] == nil {
			actions[name] = map[string]action{}
		}
		actions[name][verb] = a
	}
	for _, row := range tools.Methods() {
		if row.Access != tools.UserAccess && row.Access != tools.BoxAccess {
			continue
		}
		for _, a := range row.Actions {
			add(a.Tool, a.Name, action{method: row.Method, op: a.Op, request: a.Request, help: a.Help, readOnly: row.Scope == contract.ScopeRead})
		}
	}
	for _, a := range tools.BoxActions() {
		add(a.Tool, a.Name, action{local: a.Handler, request: a.Request, help: a.Help})
	}
	var names []string
	for name := range actions {
		names = append(names, name)
	}
	sort.Strings(names)
	var result []tool
	for _, name := range names {
		var verbs, help []string
		metadata, ok := tools.ToolkitFor(name)
		if !ok {
			return nil, nil, fmt.Errorf("missing toolkit metadata for %s", name)
		}
		readOnly := true
		props := map[string]schema{}
		takers := map[string][]string{} // the actions that take each property
		for verb := range actions[name] {
			verbs = append(verbs, verb)
		}
		sort.Strings(verbs)
		for _, verb := range verbs {
			a := actions[name][verb]
			readOnly = readOnly && a.readOnly
			help = append(help, verb+": "+a.help)
			for key, value := range requestProperties(a) {
				// A field several actions take is described once, in the toolkit's table, unless it carries its own.
				if value.Description == "" {
					value.Description = metadata.Fields[key]
				}
				if old, ok := props[key]; ok && !reflect.DeepEqual(old, value) {
					return nil, nil, fmt.Errorf("%s.%s: incompatible property %s", name, verb, key)
				}
				props[key] = value
				takers[key] = append(takers[key], verb)
			}
		}
		// The actions share one schema, and each decodes strictly, so a property that only some take says which.
		for key, by := range takers {
			if len(by) < len(verbs) {
				prop := props[key]
				prop.Description = strings.TrimSpace(prop.Description + " For " + listWords(by) + ".")
				props[key] = prop
			}
		}
		props["action"] = schema{Type: "string", Enum: verbs}
		var required []string
		if len(verbs) > 1 {
			required = []string{"action"}
		}
		if metadata.Description != "" {
			help = append([]string{metadata.Description}, help...)
		}
		result = append(result, tool{Name: name, Title: metadata.Title, Description: strings.Join(help, "\n"), InputSchema: schema{Type: "object", Properties: props, Required: required}, Annotations: annotations{ReadOnly: readOnly, Destructive: metadata.Destructive, Idempotent: metadata.Idempotent, OpenWorld: metadata.OpenWorld}})
	}
	return result, actions, nil
}

// listWords joins words as a sentence does: "a", "a and b", "a, b and c".
func listWords(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}
