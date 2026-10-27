package room

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// eventBlocks are the payload fields that the agent text appends as their own
// paragraph after the spec's one-line template, in this order. A spec with no
// blocks of its own appends detail and logTail. A block that the event leaves
// empty adds nothing, so the text has no empty section.
var eventBlocks = []string{"detail", "logTail"}

// eventText renders the spec's one-line template and appends the event's text
// blocks. A block that only repeats the template adds nothing. A block that is
// a list of strings, such as the tail lines of a shell, keeps one line per
// item.
func eventText(spec EventSpec, ev Event) string {
	text := renderEventText(spec.Text, ev)
	blocks := spec.Blocks
	if len(blocks) == 0 {
		blocks = eventBlocks
	}
	value := reflect.ValueOf(ev)
	for _, name := range blocks {
		block := strings.TrimSpace(eventBlockText(value, name))
		if block == "" || block == strings.TrimSpace(text) {
			continue
		}
		text += "\n\n" + block
	}
	return text
}

// eventBlockText formats one block field of an event. A list field, such as the
// tail lines of a shell, is one line per item.
func eventBlockText(value reflect.Value, name string) string {
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName != name || value.Field(i).Kind() != reflect.Slice {
			continue
		}
		items := value.Field(i)
		if items.Len() == 0 {
			return ""
		}
		lines := make([]string, items.Len())
		for j := range lines {
			lines[j] = fmt.Sprint(items.Index(j).Interface())
		}
		return strings.Join(lines, "\n")
	}
	return eventFieldText(value, name)
}

// eventPlaceholder matches a {field} of a spec's text template. A json name
// can hold capitals, such as {runId}.
var eventPlaceholder = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// renderEventText fills a spec's text template from the event's json fields.
func renderEventText(text string, ev Event) string {
	value := reflect.ValueOf(ev)
	return eventPlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		return eventFieldText(value, m[1:len(m)-1])
	})
}

// eventFieldText formats one json field of an event for the agent text. An
// empty list reads as "none", because a clean tree has no dirty files.
func eventFieldText(value reflect.Value, name string) string {
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName != name {
			continue
		}
		fieldValue := value.Field(i)
		switch fieldValue.Kind() {
		case reflect.String:
			// A named string type may name itself in words, as IssueActor does
			// for the engine. A plain string names itself.
			if fieldValue.CanInterface() {
				if named, ok := fieldValue.Interface().(fmt.Stringer); ok {
					return named.String()
				}
			}
			return fieldValue.String()
		case reflect.Int:
			return strconv.FormatInt(fieldValue.Int(), 10)
		case reflect.Slice:
			if fieldValue.Len() == 0 {
				return "none"
			}
			if fieldValue.Type().Elem().Kind() != reflect.String {
				return ""
			}
			items := make([]string, fieldValue.Len())
			for j := range items {
				items[j] = fieldValue.Index(j).String()
			}
			return strings.Join(items, ", ")
		}
	}
	return ""
}
