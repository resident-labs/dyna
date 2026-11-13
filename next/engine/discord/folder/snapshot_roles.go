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
		permissionNames, otherPermissions, err := splitPermissions(role.Permissions)
		if err != nil {
			return out, nil, err
		}
		name := role.Name
		// The role keeps Discord's @everyone name; overwrite references use everyone.
		out.Server.Roles = append(out.Server.Roles, Role{Managed: role.Managed, OtherPermissions: otherPermissions, Position: role.Position, Identity: Identity{ID: role.ID}, Name: name, Permissions: permissionNames, Color: fmt.Sprintf("#%06x", role.Color), Hoist: role.Hoist, Mentionable: role.Mentionable})
	}

	var unknownBits uint64
	for _, role := range out.Server.Roles {
		unknownBits |= role.OtherPermissions
	}
	if unknownBits != 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("Unknown role permission bits 0x%x are preserved as other_permissions; they are not managed.", unknownBits))
	}
	return out, roleNames, nil
}

func permissions(raw string) ([]string, error) {
	names, other, err := splitPermissions(raw)
	if err != nil {
		return nil, err
	}
	if other != 0 {
		return nil, fmt.Errorf("unsupported permission bits %x", other)
	}
	return names, nil
}

func splitPermissions(raw string) ([]string, uint64, error) {
	bits, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return nil, 0, err
	}
	var names []string
	for name, bit := range PermissionBits {
		if bits&bit != 0 {
			names = append(names, name)
			bits &^= bit
		}
	}
	sort.Slice(names, func(i, j int) bool { return PermissionBits[names[i]] < PermissionBits[names[j]] })
	return names, bits, nil
}
