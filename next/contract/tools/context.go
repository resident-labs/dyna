package tools

import "github.com/resident-labs/dyna/contract"

func contextEntries() []Entry {
	return []Entry{
		entry("context", "show", contract.MethodContextSnapshot, typ[contract.ContextSnapshotRequest](), Session, "Show a context section", "context show"),
		entry("context", "usage", contract.MethodContextSnapshot, typ[contract.ContextSnapshotRequest](), Session, "Show usage", "context usage"),
	}
}
