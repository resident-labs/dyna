package tools

import "github.com/resident-labs/dyna/contract"

func prEntries() []Entry {
	return []Entry{
		operation("pr", "open", contract.PrOpenArgs{}.OpKind(), typ[contract.PrOpenArgs](), Session, "Open a pull request with title and base. Body and draft are optional; omit head to use your session branch.", "pr open"),
	}
}
