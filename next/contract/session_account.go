package contract

// SessionAccountRequest reads a session's account choice. Only the owner can.
type SessionAccountRequest struct {
	Session SessionID `json:"session"`
}

// SessionAccountIssue says why a session runs on Automatic despite its choice.
type SessionAccountIssue string

const (
	SessionAccountRemoved     SessionAccountIssue = "removed"
	SessionAccountSignedOut   SessionAccountIssue = "signed_out"
	SessionAccountSpent       SessionAccountIssue = "spent"
	SessionAccountUnavailable SessionAccountIssue = "unavailable"
)

// SessionAccountChoice is a signed-in account that can serve the session's model.
type SessionAccountChoice struct {
	ID    AccountID `json:"id"`
	Label string    `json:"label"`
}

// SessionAccountResponse has no Account while the session is on Automatic. An Issue
// means the choice stands but the session runs on Automatic until it clears.
type SessionAccountResponse struct {
	Account AccountID              `json:"account,omitempty"`
	Label   string                 `json:"label,omitempty"`
	Issue   SessionAccountIssue    `json:"issue,omitempty"`
	Choices []SessionAccountChoice `json:"choices"`
}

// SessionSetAccountRequest chooses an account, or Automatic with no Account. A box
// cannot call this, and it never sees the choice.
type SessionSetAccountRequest struct {
	Session SessionID `json:"session"`
	Account AccountID `json:"account,omitempty"`
}
type SessionSetAccountResponse struct{}
