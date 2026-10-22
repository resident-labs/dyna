package guidance

import _ "embed"

//go:embed root.md
var rootAuthority string

//go:embed root_external.md
var rootExternal string

//go:embed root_internal.md
var rootInternal string

// RootBlock is what the holder of the room's root seat reads first: root's
// authority, then setup for the internal root session (internal) or for an
// external orchestrator. `dyna guide` prints it, and the internal root's
// session prompt starts with it.
func RootBlock(internal bool) string {
	setup := rootExternal
	if internal {
		setup = rootInternal
	}
	return rootAuthority + "\n" + setup
}
