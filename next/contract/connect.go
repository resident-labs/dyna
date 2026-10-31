package contract

const (
	MethodConnectStart  Method = "connect.start"
	MethodConnectStatus Method = "connect.status"
	MethodConnectCancel Method = "connect.cancel"
	MethodConnectUnlink Method = "connect.unlink"
)

type ConnectStartRequest struct {
	Relay string `json:"relay"`
	Name  string `json:"name"`
}
type ConnectStartResponse struct{}
type ConnectStatusRequest struct{}
type ConnectStatusResponse struct {
	Attempt          ConnectAttempt `json:"attempt"`
	URL              string         `json:"url,omitempty"`
	Code             string         `json:"code,omitempty"`
	Fingerprint      string         `json:"fingerprint,omitempty"`
	Linked           bool           `json:"linked"`
	ConnectorMissing bool           `json:"connectorMissing"`
	Routes           []string       `json:"routes"`
	Tunnel           string         `json:"tunnel,omitempty"`
}
type ConnectCancelRequest struct{}
type ConnectCancelResponse struct{}
type ConnectUnlinkRequest struct{}
type ConnectUnlinkResponse struct{}

// ConnectConnector reports a missing or restored tunnel connector.
type ConnectConnector struct {
	Missing bool `json:"missing"`
}

func (ConnectConnector) EventType() EventType { return "connect.connector" }
func (ConnectConnector) isEventBody()         {}

// ConnectAttemptState is the durable state of the owner's link attempt.
type ConnectAttemptState string

const (
	ConnectAttemptIdle      ConnectAttemptState = "idle"
	ConnectAttemptStarting  ConnectAttemptState = "starting"
	ConnectAttemptPending   ConnectAttemptState = "pending"
	ConnectAttemptLinked    ConnectAttemptState = "linked"
	ConnectAttemptUnlinking ConnectAttemptState = "unlinking"
	ConnectAttemptCanceled  ConnectAttemptState = "canceled"
	ConnectAttemptFailed    ConnectAttemptState = "failed"
)

type ConnectAttempt struct {
	State   ConnectAttemptState `json:"state"`
	Expires int64               `json:"expires"`
}
