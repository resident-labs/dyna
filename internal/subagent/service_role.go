package subagent

import (
	"fmt"

	"dyna-agent/internal/profile"
)

// RoleRoot marks a room's internal root session. It holds the room's root
// seat while no external orchestrator is attached, runs in root mode and acts
// as the room owner. Its name is "root", which no other session may take.
const RoleRoot = "root"

// RoleLibrarian keeps one repository's feature map and docs. Hooks give it
// turns; it is never a child of the session that ran the hook command. Its
// session is named LibrarianName.
const RoleLibrarian = "librarian"

// LibrarianName names the feature map and docs session, after `dyna library`.
const LibrarianName = "library"

// LibrarianSmellName names the independent code-reading smell session.
const LibrarianSmellName = "library-smells"

// RoleReviewer reviews pull requests. Profile choice ranks reviewers by
// diagnostics.
const RoleReviewer = "reviewer"

// serviceNames are the fixed session names of service roles. Only a session
// of that role may take one, so a user session never blocks a service start.
var serviceNames = map[string]string{LibrarianName: RoleLibrarian, LibrarianSmellName: RoleLibrarian}

func checkServiceName(name, role string) error {
	if owner, ok := serviceNames[name]; ok && owner != role {
		return fmt.Errorf("session name %q is reserved for the %s service", name, owner)
	}
	return nil
}

func isServiceRole(role string) bool {
	return role == RoleReviewer || role == RoleRoot || role == RoleLibrarian
}

// ManagedService reports a session that belongs to a service rather than to the
// room's hierarchy: the librarian, a PR reviewer, the voice session and their
// helpers. Such a session has no parent, no goal and no place in the member
// tree, and `dyna services` is the way to read or change it. The room's
// internal root session is not one, because it holds the room's seat.
func (m Meta) ManagedService() bool { return m.Service && m.Role != RoleRoot }

// RunOwner ties a leaf session to the workflow run and agent call it serves.
// Journal and JournalRoot are the run-owned journal that `dyna journal` writes
// for the agent; the session's environment carries both.
type RunOwner struct {
	ID          string `json:"id"`
	Agent       int    `json:"agent"`
	Journal     string `json:"journal,omitempty"`
	JournalRoot string `json:"journalRoot,omitempty"`
	// Profile is a mock-harness profile the run defined for itself, such as the
	// profiles of `dyna demo`, which are never saved to the registry. Any other
	// harness must be a registered profile.
	Profile *profile.Profile `json:"profile,omitempty"`
}

// RunOwned reports a session that a workflow run started for one agent() call.
func (m Meta) RunOwned() bool { return m.Run != nil }
