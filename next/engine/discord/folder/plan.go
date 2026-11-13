package folder

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type Change struct {
	Kind   string
	Name   string
	ID     string
	Target string
	Field  string
	Before any
	After  any
}
type Plan struct {
	Changes   []Change
	Lines     []string
	Unchanged int
}

// BuildPlan resolves identities before comparing fields. It never invents a
// replacement for an adopted object that disappeared from the server.
func BuildPlan(desired Folder, live Snapshot) (Plan, error) {
	var plan Plan
	if err := validate(desired); err != nil {
		return plan, err
	}
	compare(&plan, "server", "settings", "", desired.Server.Settings, live.Server.Settings)
	matchedRoles := map[string]bool{}
	roleOrder := []string{}
	for _, role := range desired.Server.Roles {
		candidates := []Role{}
		for _, current := range live.Server.Roles {
			if matches(role.Identity, role.Name, current.ID, current.Name) {
				candidates = append(candidates, current)
			}
		}
		if len(candidates) > 1 {
			return plan, fmt.Errorf("server.yaml: role %q is not unique", role.Name)
		}
		if len(candidates) == 0 {
			if role.ID != "" || role.Was != "" {
				return plan, fmt.Errorf("server.yaml: adopted role %q is missing", role.Name)
			}
			plan.Changes = append(plan.Changes, Change{Kind: "role", Name: role.Name, Field: "create", After: role})
			continue
		}
		current := candidates[0]
		if matchedRoles[current.ID] {
			return plan, fmt.Errorf("server.yaml: role %s adopted twice", current.ID)
		}
		matchedRoles[current.ID] = true
		roleOrder = append(roleOrder, current.ID)
		role.Identity, current.Identity = Identity{}, Identity{}
		compare(&plan, "role", role.Name, candidates[0].ID, role, current)
	}
	for _, role := range live.Server.Roles {
		if !matchedRoles[role.ID] {
			plan.Lines = append(plan.Lines, fmt.Sprintf("Role %s is on the server, not in the folder.", role.Name))
		}
	}
	if err := planAutoMod(&plan, desired.Server.AutoMod, live.Server.AutoMod); err != nil {
		return plan, err
	}
	desiredOrder, liveOrder := roleOrder, []string{}
	for _, role := range live.Server.Roles {
		if matchedRoles[role.ID] {
			liveOrder = append(liveOrder, role.ID)
		}
	}
	if !slices.Equal(desiredOrder, liveOrder) {
		plan.Lines = append(plan.Lines, "Role order must be changed by hand if the bot's highest role is not above every role it must manage.")
		compare(&plan, "server", "role order", "", desiredOrder, liveOrder)
	}
	resolvedParents := map[string]string{"": ""}
	for _, entry := range desired.Entries {
		if entry.Category == nil {
			continue
		}
		resolvedParents[entry.Name] = entry.Name
		for _, current := range live.Entries {
			if current.Category != nil && matches(entry.Category.Identity, entry.Name, entryID(current), current.Name) {
				resolvedParents[entry.Name] = current.Name
			}
		}
	}
	matchedEntries := map[string]bool{}
	ignoredParents := map[string]bool{}
	// Identify unmanaged categories before considering their children.
	for _, entry := range desired.Entries {
		if entry.Unmanaged == "" {
			continue
		}
		plan.Lines = append(plan.Lines, fmt.Sprintf("%s is unmanaged: %s", entry.Path, entry.Unmanaged))
		for _, current := range live.Entries {
			if entry.Path == current.Path {
				matchedEntries[entryID(current)] = true
				if current.Category != nil {
					ignoredParents[current.Path] = true
				}
			}
		}
	}
	for _, entry := range desired.Entries {
		if entry.Unmanaged != "" {
			continue
		}
		candidates := []Entry{}
		for _, current := range live.Entries {
			if ignoredParents[current.Parent] {
				continue
			}
			if (entry.Category == nil) != (current.Category == nil) {
				continue
			}
			if matches(entryIdentity(entry), entry.Name, entryID(current), current.Name) && (entryIdentity(entry).ID != "" || resolvedParents[entry.Parent] == current.Parent) {
				candidates = append(candidates, current)
			}
		}
		if len(candidates) > 1 {
			return plan, fmt.Errorf("%s: name is not unique", entry.Path)
		}
		kind := "channel"
		if entry.Category != nil {
			kind = "category"
		}
		if len(candidates) == 0 {
			identity := entryIdentity(entry)
			if identity.ID != "" || identity.Was != "" {
				return plan, fmt.Errorf("%s: adopted %s is missing", entry.Path, kind)
			}
			plan.Changes = append(plan.Changes, Change{Kind: kind, Name: entry.Path, Field: "create", After: entry})
			planMessages(&plan, entry, Entry{})
			continue
		}
		current := candidates[0]
		id := entryID(current)
		if matchedEntries[id] {
			return plan, fmt.Errorf("%s: object %s is already adopted or unmanaged", entry.Path, id)
		}
		matchedEntries[id] = true
		compare(&plan, kind, entry.Path, id, struct{ Name, Parent string }{entry.Name, resolvedParents[entry.Parent]}, struct{ Name, Parent string }{current.Name, current.Parent})
		if entry.Category != nil {
			desiredCategory, liveCategory := *entry.Category, *current.Category
			desiredCategory.Identity, liveCategory.Identity = Identity{}, Identity{}
			compare(&plan, kind, entry.Path, id, desiredCategory, liveCategory)
		} else {
			desiredChannel, liveChannel := *entry.Channel, *current.Channel
			desiredChannel.Identity, liveChannel.Identity = Identity{}, Identity{}
			compare(&plan, kind, entry.Path, id, desiredChannel, liveChannel)
		}
		planMessages(&plan, entry, current)
	}
	for _, entry := range live.Entries {
		if !matchedEntries[entryID(entry)] && !ignoredParents[entry.Parent] {
			plan.Lines = append(plan.Lines, fmt.Sprintf("%s is on the server, not in the folder.", entry.Path))
		}
	}
	compare(&plan, "server", "category order", "", desired.Server.Categories, live.Server.Categories)
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
		return identity.Was == currentName
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

func compare(plan *Plan, kind, name, id string, desired, current any) {
	if reflect.DeepEqual(desired, current) {
		plan.Unchanged++
		return
	}
	desiredValue, currentValue := reflect.ValueOf(desired), reflect.ValueOf(current)
	if desiredValue.Kind() != reflect.Struct {
		plan.Changes = append(plan.Changes, Change{Kind: kind, Name: name, ID: id, Field: "value", Before: current, After: desired})
		return
	}
	for index := 0; index < desiredValue.NumField(); index++ {
		field := desiredValue.Type().Field(index)
		if field.Name == "Identity" {
			continue
		}
		before, after := currentValue.Field(index).Interface(), desiredValue.Field(index).Interface()
		if reflect.DeepEqual(before, after) {
			continue
		}
		label := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if label == "" {
			label = field.Name
		}
		plan.Changes = append(plan.Changes, Change{Kind: kind, Name: name, ID: id, Field: label, Before: before, After: after})
	}
}

func (plan Plan) String() string {
	lines := slices.Clone(plan.Lines)
	for _, change := range plan.Changes {
		if change.Field == "create" {
			lines = append(lines, fmt.Sprintf("Create %s %s.", change.Kind, change.Name))
		} else {
			lines = append(lines, fmt.Sprintf("Change %s %s %s: %v → %v.", change.Kind, change.Name, change.Field, change.Before, change.After))
		}
	}
	lines = append(lines, fmt.Sprintf("%d changes; %d unchanged.", len(plan.Changes), plan.Unchanged))
	return strings.Join(lines, "\n")
}
