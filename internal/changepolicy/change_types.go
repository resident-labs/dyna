package changepolicy

type Policy struct {
	Reviewers        []string          `json:"reviewers,omitempty"`
	CITimeBudget     string            `json:"ciTimeBudget,omitempty"`
	CIBudgetExempt   []string          `json:"ciBudgetExempt,omitempty"`
	CIWarnAfter      string            `json:"ciWarnAfter,omitempty"`
	CIFailAfter      string            `json:"ciFailAfter,omitempty"`
	RequiredChecks   []string          `json:"requiredChecks" tstype:"string[] | null"`
	Review           string            `json:"review"`
	CINoJUnitWarning *bool             `json:"ciNoJUnitWarning,omitempty"`
	CITestMax        string            `json:"ciTestMax,omitempty"`
	CITestAllowNotes map[string]string `json:"ciTestAllowNotes,omitempty"`
	CITestAllow      []string          `json:"ciTestAllow,omitempty"`
}

// Override records both the changed settings and the author's reason.
// A nil RequiredChecks inherits the room list; an empty list overrides it.
type Override struct {
	RequiredChecks []string `json:"requiredChecks" tstype:"string[] | null"`
	Review         string   `json:"review,omitempty"`
	Reason         string   `json:"reason"`
}
