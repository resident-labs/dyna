package tools

import "github.com/resident-labs/dyna/contract"

func contextEntries() []Entry {
	return []Entry{
		entry("context", "show", contract.MethodContextSnapshot, typ[contract.ContextSnapshotRequest](), Session, "Show a full context section by section key. Omit section to show all sections.", "context show"),
	}
}
