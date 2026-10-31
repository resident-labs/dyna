package tools

import "github.com/resident-labs/dyna/contract"

func prEntries() []Entry {
	return []Entry{
		operation("pr", "open", contract.PrOpenArgs{}.OpKind(), typ[contract.PrOpenArgs](), Session, "Open a pull request", "pr open"),
	}
}
