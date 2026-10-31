package contract

import "time"

type AccountID string

type AccountAction string

const (
	AccountActionConnect  AccountAction = "connect"
	AccountActionCallback AccountAction = "callback"
	AccountActionSetKey   AccountAction = "set_key"
	AccountActionComplete AccountAction = "complete"
)

// AccountsActionArgs contains only an ephemeral ticket, never account secrets.
type AccountsActionArgs struct {
	Action AccountAction `json:"action"`
	Ticket string        `json:"ticket"`
}

func (AccountsActionArgs) OpKind() OpKind { return "accounts.action" }
func (AccountsActionArgs) isOpArgs()      {}

type ConnectMethod string

const (
	ConnectOAuth ConnectMethod = "oauth"
	ConnectKey   ConnectMethod = "key"
)

type AccountState string

const (
	AccountConnected     AccountState = "connected"
	AccountSignInExpired AccountState = "sign_in_expired"
	AccountQuotaLowState AccountState = "quota_low"
)

type AccountProviderInfo struct {
	ID            AccountProvider `json:"id"`
	DisplayName   string          `json:"displayName"`
	ConnectMethod ConnectMethod   `json:"connectMethod"`
}
type Account struct {
	ID       AccountID       `json:"id"`
	Provider AccountProvider `json:"provider"`
	Label    string          `json:"label"`
	Rooms    []RoomID        `json:"rooms"`
	State    AccountState    `json:"state"`
}
type AccountsListRequest struct{}
type AccountsListResponse struct {
	Providers []AccountProviderInfo `json:"providers"`
	Accounts  []Account             `json:"accounts"`
}
type ConnectFlow string

const (
	ConnectFlowCallback ConnectFlow = "callback"
	ConnectFlowDevice   ConnectFlow = "device"
)

type ConnectStatus string

const (
	ConnectPending   ConnectStatus = "pending"
	ConnectConnected ConnectStatus = "connected"
	ConnectFailed    ConnectStatus = "failed"
	ConnectExpired   ConnectStatus = "expired"
)

type AccountsConnectRequest struct {
	Provider AccountProvider `json:"provider"`
	Label    string          `json:"label"`
}
type AccountsConnectResponse struct {
	Attempt   string      `json:"attempt"`
	Flow      ConnectFlow `json:"flow"`
	Link      string      `json:"link"`
	Code      string      `json:"code,omitempty"`
	ExpiresAt time.Time   `json:"expiresAt"`
}
type AccountsConnectCallbackRequest struct {
	Attempt     string `json:"attempt"`
	RedirectURL string `json:"redirectUrl"`
}
type AccountsConnectCallbackResponse struct{}
type AccountsConnectStatusRequest struct {
	Attempt string `json:"attempt"`
}
type AccountsConnectStatusResponse struct {
	Status    ConnectStatus `json:"status"`
	AccountID AccountID     `json:"accountId,omitempty"`
}
type AccountsSetKeyRequest struct {
	Provider AccountProvider `json:"provider"`
	Label    string          `json:"label"`
	Key      string          `json:"key"`
}
type AccountsSetKeyResponse struct {
	AccountID AccountID `json:"accountId"`
}
type AccountsRemoveRequest struct {
	AccountID AccountID `json:"accountId"`
}
type AccountsRemoveResponse struct{}
type AccountsSetLimitsRequest struct {
	AccountID AccountID `json:"accountId"`
	Rooms     []RoomID  `json:"rooms"`
}
type AccountsSetLimitsResponse struct{}
type Profile struct {
	Name           ProfileName   `json:"name"`
	Provider       ModelProvider `json:"provider"`
	Model          string        `json:"model"`
	Thinking       ThinkingLevel `json:"thinking"`
	AllowedModels  []string      `json:"allowedModels"`
	CompactTokens  int           `json:"compactTokens"`
	MaxConcurrent  int           `json:"maxConcurrent"`
	MaxCallsPerRun int           `json:"maxCallsPerRun"`
}
type ProfileSetRequest struct {
	Profile Profile `json:"profile"`
}
type ProfileSetResponse struct{}
type ProfileRemoveRequest struct {
	Name ProfileName `json:"name"`
}
type ProfileRemoveResponse struct{}
type AccountFailureReason string

const (
	AccountSignInRejected AccountFailureReason = "sign_in_rejected"
	AccountRefreshFailed  AccountFailureReason = "refresh_failed"
)

type AccountFailed struct {
	AccountID AccountID            `json:"accountId"`
	Reason    AccountFailureReason `json:"reason"`
}

func (AccountFailed) EventType() EventType { return "account.failed" }
func (AccountFailed) isEventBody()         {}

type AccountQuotaLow struct {
	AccountID        AccountID `json:"accountId"`
	Window           string    `json:"window"`
	RemainingPercent float64   `json:"remainingPercent"`
	Floor            float64   `json:"floor"`
}

func (AccountQuotaLow) EventType() EventType { return "account.quota_low" }
func (AccountQuotaLow) isEventBody()         {}
