package folder

import (
	"fmt"
	"sort"
	"strconv"
)

func snapshotRoles(out Snapshot, roles []liveRole, server string) (Snapshot, map[string]string, error) {
	roleNames := map[string]string{server: "everyone"}
	names := map[string]bool{"everyone": true}
	sort.SliceStable(roles, func(i, j int) bool { return roles[i].Position > roles[j].Position })
	for _, role := range roles {
		if role.ID == server {
			roleNames[role.ID] = "everyone"
		} else {
			if names[role.Name] {
				return out, nil, fmt.Errorf("ambiguous role %q", role.Name)
			}
			names[role.Name] = true
			roleNames[role.ID] = role.Name
		}
		if role.Icon != nil || role.UnicodeEmoji != nil {
			return out, nil, fmt.Errorf("role icons cannot be represented")
		}
		permissionNames, err := permissions(role.Permissions)
		if err != nil {
			return out, nil, err
		}
		name := role.Name
		// The role keeps Discord's @everyone name; overwrite references use everyone.
		out.Server.Roles = append(out.Server.Roles, Role{Position: role.Position, Identity: Identity{ID: role.ID}, Name: name, Permissions: permissionNames, Color: fmt.Sprintf("#%06x", role.Color), Hoist: role.Hoist, Mentionable: role.Mentionable})
	}

	return out, roleNames, nil
}

func permissions(raw string) ([]string, error) {
	bits, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, bit := range PermissionBits {
		if bits&bit != 0 {
			names = append(names, name)
			bits &^= bit
		}
	}
	sort.Slice(names, func(i, j int) bool { return PermissionBits[names[i]] < PermissionBits[names[j]] })
	if bits != 0 {
		return nil, fmt.Errorf("unsupported permission bits %x", bits)
	}
	return names, nil
}
