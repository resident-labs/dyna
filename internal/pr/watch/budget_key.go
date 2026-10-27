package watch

import "strings"

// budgetFailurePrefix marks a CI result that failed a time budget.
// Notice tokens use ci-budget: and are a different family.
const budgetFailurePrefix = "budget:"

// BudgetFailureKey is the CI key of one time-budget failure.
type BudgetFailureKey struct {
	head, token string
	raw         string
}

// NewBudgetFailureKey builds the CI key applyCI stores for a budget failure.
func NewBudgetFailureKey(head, token string) BudgetFailureKey {
	return BudgetFailureKey{head: head, token: token, raw: budgetFailurePrefix + head + ":" + token}
}

// String is the stored CI key.
func (k BudgetFailureKey) String() string { return k.raw }

// Head is the commit this failure applies to.
func (k BudgetFailureKey) Head() string { return k.head }

// Token is the run token this failure applies to.
func (k BudgetFailureKey) Token() string { return k.token }

// ParseBudgetFailureKey reads a budget-failure CI key. It accepts every key
// the historical "budget:" prefix check accepted, including a bare prefix.
func ParseBudgetFailureKey(raw string) (BudgetFailureKey, bool) {
	rest, ok := strings.CutPrefix(raw, budgetFailurePrefix)
	if !ok {
		return BudgetFailureKey{}, false
	}
	head, token, _ := strings.Cut(rest, ":")
	return BudgetFailureKey{head: head, token: token, raw: raw}, true
}

// BudgetFailureNamesHead reports whether raw is the budget result for head.
// It matches the historical prefix budget:<head>:.
func BudgetFailureNamesHead(raw, head string) bool {
	return strings.HasPrefix(raw, NewBudgetFailureKey(head, "").String())
}
