// Package changepolicy defines GitHub CI and review settings.
package changepolicy

import (
	"fmt"
	"strings"
	"time"
)

type Policy struct {
	Reviewers      []string `json:"reviewers,omitempty"`
	CITimeBudget   string   `json:"ciTimeBudget,omitempty"`
	CIBudgetExempt []string `json:"ciBudgetExempt,omitempty"`
	CIWarnAfter    string   `json:"ciWarnAfter,omitempty"`
	CIFailAfter    string   `json:"ciFailAfter,omitempty"`
	RequiredChecks []string `json:"requiredChecks"`
	Review         string   `json:"review"`
	// TODO: legacy/shim remove later — reads saved landing modes for old policy documents and clients; remove after those policies migrate.
	Land             string            `json:"land"`
	CINoJUnitWarning *bool             `json:"ciNoJUnitWarning,omitempty"`
	CITestMax        string            `json:"ciTestMax,omitempty"`
	CITestAllowNotes map[string]string `json:"ciTestAllowNotes,omitempty"`
	CITestAllow      []string          `json:"ciTestAllow,omitempty"`
}

// Override records both the changed settings and the author's reason.
// A nil RequiredChecks inherits the room list; an empty list overrides it.
type Override struct {
	RequiredChecks []string `json:"requiredChecks"`
	Review         string   `json:"review,omitempty"`
	// TODO: legacy/shim remove later — reads saved landing overrides for old policy documents and clients; remove after those policies migrate.
	Land   string `json:"land,omitempty"`
	Reason string `json:"reason"`
}

func Default() Policy {
	return Policy{CITimeBudget: "warn", CIWarnAfter: "2m", CIFailAfter: "3m", CITestMax: "10s", RequiredChecks: []string{}, Review: "on", Land: "github-pr"}
}
func (o Override) Empty() bool {
	return o.RequiredChecks == nil && o.Review == "" && o.Land == ""
}
func Apply(p Policy, o Override) Policy {
	if o.RequiredChecks != nil {
		p.RequiredChecks = append([]string{}, o.RequiredChecks...)
	}
	if o.Review != "" {
		p.Review = o.Review
	}
	if o.Land != "" {
		p.Land = o.Land
	}
	return p
}
func Validate(p Policy) error {
	if !oneOf(p.CITimeBudget, "", "enforce", "warn") {
		return fmt.Errorf("CI time budget must be enforce or warn")
	}
	if _, _, err := p.CIBudget(); err != nil {
		return err
	}
	if _, _, err := p.TestBudget(); err != nil {
		return err
	}
	seenReviewers := map[string]bool{}
	for _, id := range p.Reviewers {
		if id == "" || strings.TrimSpace(id) != id || seenReviewers[id] {
			return fmt.Errorf("reviewers must be distinct session IDs")
		}
		seenReviewers[id] = true
	}
	if !oneOf(p.Review, "on", "off") {
		return fmt.Errorf("review must be on or off")
	}
	if !oneOf(p.Land, "github-pr", "local-merge", "branch-only") {
		return fmt.Errorf("land must be github-pr, local-merge, or branch-only")
	}
	if len(p.RequiredChecks) > 20 {
		return fmt.Errorf("at most 20 required checks are allowed")
	}
	seen := map[string]bool{}
	for _, name := range p.RequiredChecks {
		if strings.TrimSpace(name) != name || name == "" || len(name) > 120 || strings.ContainsAny(name, "\x00\n\r") {
			return fmt.Errorf("invalid required check name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate required check %q", name)
		}
		seen[name] = true
	}
	return nil
}
func ValidateOverride(o Override) error {
	if !o.Empty() && strings.TrimSpace(o.Reason) == "" {
		return fmt.Errorf("a policy override requires a reason")
	}
	if len(o.Reason) > 4000 {
		return fmt.Errorf("policy override reason exceeds 4000 bytes")
	}
	return Validate(Apply(Default(), o))
}
func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
func SkipReasons(p Policy, o Override) []string {
	reasons := []string{}
	if p.Review == "off" {
		reasons = append(reasons, "Review is off in the effective policy.")
	}
	if !o.Empty() {
		reasons = append(reasons, "Change override: "+o.Reason)
	}
	return reasons
}

// CIBudget supplies defaults for rooms created before budget settings existed.
// TODO: legacy/shim remove later — remove zero-value fallback after room policies persist budgets.
func (p Policy) CIBudget() (time.Duration, time.Duration, error) {
	if len(p.CIBudgetExempt) > 20 {
		return 0, 0, fmt.Errorf("at most 20 CI budget exemptions are allowed")
	}
	seen := map[string]bool{}
	for _, name := range p.CIBudgetExempt {
		if name == "" || strings.TrimSpace(name) != name || len(name) > 120 || strings.ContainsAny(name, "\x00\n\r") {
			return 0, 0, fmt.Errorf("invalid CI budget exemption %q", name)
		}
		if seen[name] {
			return 0, 0, fmt.Errorf("duplicate CI budget exemption %q", name)
		}
		seen[name] = true
	}
	warn, fail := p.CIWarnAfter, p.CIFailAfter
	if warn == "" {
		warn = "2m"
	}
	if fail == "" {
		fail = "3m"
	}
	w, e := time.ParseDuration(warn)
	if e != nil {
		return 0, 0, fmt.Errorf("invalid CI warn duration: %w", e)
	}
	f, e := time.ParseDuration(fail)
	if e != nil {
		return 0, 0, fmt.Errorf("invalid CI fail duration: %w", e)
	}
	if w <= 0 || f <= w {
		return 0, 0, fmt.Errorf("CI budget requires 0 < warn < fail")
	}
	return w, f, nil
}

// CIBudgetApplies leaves exempt workflows in the CI gate without timing them.
func (p Policy) CIBudgetApplies(workflow string) bool {
	for _, exempt := range p.CIBudgetExempt {
		if exempt == workflow {
			return false
		}
	}
	return true
}

// TestBudget supplies the room's individual test limit and exact allowlist.
// TODO: legacy/shim remove later — remove fallback after all room policies persist test budgets.
func (p Policy) TestBudget() (time.Duration, map[string]bool, error) {
	value := p.CITestMax
	if value == "" {
		value = "10s"
	}
	max, err := time.ParseDuration(value)
	if err != nil || max <= 0 {
		return 0, nil, fmt.Errorf("test budget must be a positive duration")
	}
	if len(p.CITestAllow) > 1000 {
		return 0, nil, fmt.Errorf("at most 1000 allowed tests are supported")
	}
	allowed := make(map[string]bool, len(p.CITestAllow))
	for _, name := range p.CITestAllow {
		if name == "" || strings.TrimSpace(name) != name || len(name) > 1000 || strings.ContainsAny(name, "\x00\r\n") {
			return 0, nil, fmt.Errorf("invalid allowed test name %q", name)
		}
		allowed[name] = true
	}
	return max, allowed, nil
}

// WarnNoJUnit defaults to enabled for rooms without an explicit setting.
// TODO: legacy/shim remove later — remove nil fallback after room policies persist this setting.
func (p Policy) WarnNoJUnit() bool { return p.CINoJUnitWarning == nil || *p.CINoJUnitWarning }

// EnforceCITimeBudget reports whether timing can fail or cancel CI.
func (p Policy) EnforceCITimeBudget() bool { return p.CITimeBudget == "enforce" }
