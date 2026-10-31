package tools

import "github.com/resident-labs/dyna/contract"

func hostEntries() []Entry {
	return []Entry{
		entry("host", "add", contract.MethodHostAdd, typ[contract.HostAddRequest](), Owner, "Add a host", "host add"),
	}
}
