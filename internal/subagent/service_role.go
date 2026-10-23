package subagent

import "fmt"

// RoleRoot marks a room's internal root session. It holds the room's root
// seat while no external orchestrator is attached, runs in root mode and acts
// as the room owner. Its name is "root", which no other session may take.
const RoleRoot = "root"

// RoleLibrarian keeps one repository's feature map and docs. Hooks give it
// turns; it is never a child of the session that ran the hook command. Its
// session is named LibrarianName.
const RoleLibrarian = "librarian"

// LibrarianName names the librarian session, after `dyna library`.
const LibrarianName = "library"

// RoleSteward owns recovery decisions for failed merge batches.
const RoleSteward = "merge-steward"

// serviceNames are the fixed session names of service roles. Only a session
// of that role may take one, so a user session never blocks a service start.
var serviceNames = map[string]string{LibrarianName: RoleLibrarian, RoleSteward: RoleSteward}

func checkServiceName(name, role string) error {
	if owner, ok := serviceNames[name]; ok && owner != role {
		return fmt.Errorf("session name %q is reserved for the %s service", name, owner)
	}
	return nil
}

func isServiceRole(role string) bool {
	return role == "reviewer" || role == RoleRoot || role == RoleLibrarian || role == RoleSteward
}
