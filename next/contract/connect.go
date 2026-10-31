package contract

// ConnectAttemptState is the durable state of the owner's link attempt.
type ConnectAttemptState string

const (
	ConnectAttemptStarting ConnectAttemptState = "starting"
	ConnectAttemptPending  ConnectAttemptState = "pending"
	ConnectAttemptLinked   ConnectAttemptState = "linked"
	ConnectAttemptCanceled ConnectAttemptState = "canceled"
	ConnectAttemptFailed   ConnectAttemptState = "failed"
)

type ConnectAttempt struct {
	State   ConnectAttemptState `json:"state"`
	Expires int64               `json:"expires"`
}
