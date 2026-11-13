package folder

import (
	"fmt"
	"slices"
	"strings"
)

func planRoles(plan *Plan, desired []Role, live Snapshot) error {
	matched := map[string]bool{}
	desiredOrder := []string{}
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
			if role.ID != "" || role.Was != "" {
				return fmt.Errorf("server.yaml: adopted role %q is missing", role.Name)
			}
			plan.Changes = append(plan.Changes, Change{Kind: RoleKind, Operation: Create, Name: role.Name, After: role})
			continue
		}
		current := candidates[0]
		if matched[current.ID] {
			return fmt.Errorf("server.yaml: role %s adopted twice", current.ID)
		}
		matched[current.ID] = true
		desiredOrder = append(desiredOrder, current.ID)
		role.Identity, current.Identity = Identity{}, Identity{}
		compare(plan, RoleKind, role.Name, candidates[0].ID, role, current)
	}
	liveOrder, blocked := []string{}, []string{}
	for _, role := range live.Server.Roles {
		if !matched[role.ID] {
			plan.Lines = append(plan.Lines, fmt.Sprintf("Role %s is on the server, not in the folder.", role.Name))
			continue
		}
		liveOrder = append(liveOrder, role.ID)
		if role.Position >= live.BotRolePosition {
			blocked = append(blocked, role.Name)
		}
	}
	if !slices.Equal(desiredOrder, liveOrder) {
		if len(blocked) > 0 {
			plan.Lines = append(plan.Lines, fmt.Sprintf("Change role order by hand: %s sit at or above the bot's highest role.", strings.Join(blocked, ", ")))
		} else {
			compare(plan, ServerKind, "role order", "", desiredOrder, liveOrder)
		}
	}
	return nil
}
