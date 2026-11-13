package folder

import (
	"reflect"
	"slices"
)

// Live messages are supplied by the ownership record, never by reading history.
// The filename is the stable record key; ID is the existing Discord message.
func planMessages(plan *Plan, desired, current Entry) {
	names := make([]string, 0, len(desired.Messages))
	for name := range desired.Messages {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		message := desired.Messages[name]
		before, exists := current.Messages[name]
		id := before.ID
		message.ID, before.ID = "", ""
		if exists && reflect.DeepEqual(message, before) {
			plan.Unchanged++
			continue
		}
		field := "create"
		if exists {
			field = "edit"
		}
		plan.Changes = append(plan.Changes, Change{Kind: "message", Name: desired.Path + "/" + name, ID: id, Target: entryID(current), Field: field, Before: before, After: message})
	}
}
