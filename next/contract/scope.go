package contract

type Scope string

const (
	ScopeRead      Scope = "read"
	ScopeOperate   Scope = "operate"
	ScopeFiles     Scope = "files"
	ScopeTerminal  Scope = "terminal"
	ScopeComputers Scope = "computers"
	ScopeAdmin     Scope = "admin"
)
