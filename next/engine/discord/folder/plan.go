package folder

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type Kind string

const (
	ServerKind   Kind = "server"
	RoleKind     Kind = "role"
	CategoryKind Kind = "category"
	ChannelKind  Kind = "channel"
	AutoModKind  Kind = "automod"
	MessageKind  Kind = "message"
	PolicyKind   Kind = "policy"
)

type Operation string

const (
	Create       Operation = "create"
	ChangeFields Operation = "change"
	EditMessage  Operation = "edit message"
)

type Change struct {
	Kind      Kind
	Operation Operation
	Name      string
	ID        string
	Target    string
	Field     string
	Before    any
	After     any
}
type Plan struct {
	Changes   []Change
	Lines     []string
	Unchanged int
}

// BuildPlan resolves identities before comparing fields. Missing adopted objects
// refuse rather than silently becoming replacements.
func BuildPlan(desired Folder, live Snapshot) (Plan, error) {
	var plan Plan
	if err := validate(desired); err != nil {
		return plan, err
	}
	compare(&plan, ServerKind, "settings", "", desired.Server.Settings, live.Server.Settings)
	if err := planRoles(&plan, desired.Server.Roles, live); err != nil {
		return plan, err
	}
	if err := planAutoMod(&plan, desired.Server.AutoMod, live.Server.AutoMod); err != nil {
		return plan, err
	}
	if err := planEntries(&plan, desired.Entries, live.Entries); err != nil {
		return plan, err
	}
	compare(&plan, ServerKind, "category order", "", desired.Server.Categories, live.Server.Categories)
	if desired.Policy != "" {
		compare(&plan, PolicyKind, "policy.yaml", "", desired.Policy, live.Policy)
	}
	if desired.Hooks {
		plan.Lines = append(plan.Lines, "hooks.yaml is not applied yet")
	}
	return plan, nil
}

func matches(identity Identity, name, currentID, currentName string) bool {
	if identity.ID != "" {
		return identity.ID == currentID
	}
	if identity.Was != "" {
		return identity.Was == currentName || name == currentName
	}
	return name == currentName
}
func entryIdentity(entry Entry) Identity {
	if entry.Category != nil {
		return entry.Category.Identity
	}
	if entry.Channel != nil {
		return entry.Channel.Identity
	}
	return Identity{}
}
func entryID(entry Entry) string { return entryIdentity(entry).ID }

func compare(plan *Plan, kind Kind, name, id string, desired, current any) {
	if reflect.DeepEqual(desired, current) {
		plan.Unchanged++
		return
	}
	desiredValue, currentValue := reflect.ValueOf(desired), reflect.ValueOf(current)
	if desiredValue.Kind() != reflect.Struct {
		plan.Changes = append(plan.Changes, Change{Kind: kind, Operation: ChangeFields, Name: name, ID: id, Field: "value", Before: current, After: desired})
		return
	}
	for index := 0; index < desiredValue.NumField(); index++ {
		field := desiredValue.Type().Field(index)
		label := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if field.Name == "Identity" || label == "-" {
			continue
		}
		before, after := currentValue.Field(index).Interface(), desiredValue.Field(index).Interface()
		if reflect.DeepEqual(before, after) {
			continue
		}
		if label == "" {
			label = field.Name
		}
		plan.Changes = append(plan.Changes, Change{Kind: kind, Operation: ChangeFields, Name: name, ID: id, Field: label, Before: before, After: after})
	}
}

func (plan Plan) String() string {
	lines := slices.Clone(plan.Lines)
	for _, change := range plan.Changes {
		switch change.Operation {
		case Create:
			lines = append(lines, fmt.Sprintf("Create %s %s.", change.Kind, change.Name))
		case EditMessage:
			lines = append(lines, fmt.Sprintf("Edit message %s in place (%s).", change.Name, change.ID))
		case ChangeFields:
			lines = append(lines, fmt.Sprintf("Change %s %s %s: %v → %v.", change.Kind, change.Name, change.Field, change.Before, change.After))
		}
	}
	lines = append(lines, fmt.Sprintf("%d changes; %d unchanged.", len(plan.Changes), plan.Unchanged))
	return strings.Join(lines, "\n")
}
