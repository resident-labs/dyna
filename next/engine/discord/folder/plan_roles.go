package folder

import (
	"fmt"
	"slices"
	"strings"
)

// RoleReference names a newly created role until its Discord ID is recorded.
type RoleReference struct {
	ID   string
	Name string
}

func planRoles(plan *Plan, desired []Role, live Snapshot) error {
	matched := map[string]bool{}
	desiredOrder := []RoleReference{}
	for _, role := range desired {
		candidates := []Role{}
		for _, current := range live.Server.Roles {
			if matches(role.Identity, role.Name, current.ID, current.Name) {
				candidates = append(candidates, current)
			}
		}
		if len(candidates) > 1 {
			return fmt.Errorf("server.yaml: role %q is not unique", role.Name)
		}
		if len(candidates) == 0 {
			if role.Managed || role.ID != "" || role.Was != "" {
				return fmt.Errorf("server.yaml: adopted role %q is missing", role.Name)
			}
			plan.Changes = append(plan.Changes, Change{Kind: RoleKind, Operation: Create, Name: role.Name, After: role})
			desiredOrder = append(desiredOrder, RoleReference{Name: role.Name})
			continue
		}
		current := candidates[0]
		if matched[current.ID] {
			return fmt.Errorf("server.yaml: role %s adopted twice", current.ID)
		}
		matched[current.ID] = true
		plan.resolve(Subject{RoleKind, role.Name}, current.ID)
		desiredOrder = append(desiredOrder, RoleReference{ID: current.ID})
		if current.Managed != role.Managed {
			return fmt.Errorf("server.yaml: role %q has managed: %v in the folder but %v on the server", role.Name, role.Managed, current.Managed)
		}
		if current.Managed || role.Managed {
			continue
		}
		// Opaque live bits are not owned by the folder and must survive edits.
		role.OtherPermissions = current.OtherPermissions
		for _, color := range []*string{&role.Color, &current.Color} {
			if *color == "" {
				*color = "#000000" // Discord reports no colour this way
			}
		}
		role.Identity, current.Identity = Identity{}, Identity{}
		compare(plan, RoleKind, role.Name, candidates[0].ID, role, current)
	}
	liveOrder, blocked := []RoleReference{}, []string{}
	managedMoved := false
	for _, role := range live.Server.Roles {
		if !matched[role.ID] {
			plan.Lines = append(plan.Lines, fmt.Sprintf("Role %s is on the server, not in the folder.", role.Name))
			continue
		}
		currentIndex := len(liveOrder)
		liveOrder = append(liveOrder, RoleReference{ID: role.ID})
		desiredIndex := slices.Index(desiredOrder, RoleReference{ID: role.ID})
		if currentIndex != desiredIndex && (role.Managed || role.Position >= live.BotRolePosition) {
			blocked = append(blocked, role.Name)
			managedMoved = managedMoved || role.Managed
		}
	}
	if !slices.Equal(desiredOrder, liveOrder) {
		if len(blocked) > 0 {
			reason := "sit at or above the bot's highest role"
			if managedMoved {
				reason = "are managed or sit at or above the bot's highest role"
			}
			plan.Lines = append(plan.Lines, fmt.Sprintf("Change role order by hand: %s %s.", strings.Join(blocked, ", "), reason))
		} else {
			compare(plan, ServerKind, "role order", "", desiredOrder, liveOrder)
		}
	}
	return nil
}
