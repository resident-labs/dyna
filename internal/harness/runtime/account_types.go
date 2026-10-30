package runtime

// AccountProblemReason is why one saved row could not be listed.
type AccountProblemReason string

const (
	// AccountUnknownHarness names a row of a harness this release does not run.
	AccountUnknownHarness AccountProblemReason = "unknown-harness"
	// AccountUnsafeHome names a row whose saved home is unsafe or gone.
	AccountUnsafeHome AccountProblemReason = "unsafe-home"
	// AccountMissingHome names a row that names no saved home.
	AccountMissingHome AccountProblemReason = "missing-home"
	// AccountUnreadable names a row that could not be read for another reason.
	AccountUnreadable AccountProblemReason = "unreadable"
)

// AccountProblem is one saved account row that the list skipped. It is a result
// about that row, never a reason to fail the whole list: one bad row must not
// hide every other account.
type AccountProblem struct {
	ID      string               `json:"id"`
	Harness string               `json:"harness,omitempty"`
	Label   string               `json:"label,omitempty"`
	Reason  AccountProblemReason `json:"reason"`
}
