package workflowcatalog

import "dyna-agent/internal/plan"

type Scope string

const (
	ScopeProject  Scope = "project"
	ScopePersonal Scope = "personal"
	ScopeBundled  Scope = "bundled"
)

type Info struct {
	Name        string             `json:"name"`
	Scope       Scope              `json:"scope"`
	Description string             `json:"description"`
	Args        []plan.WorkflowArg `json:"args"`
}
