package contract

import "time"

type AccountID string

type AccountAction string

const (
	AccountActionConnect   AccountAction = "connect"
	AccountActionReconnect AccountAction = "reconnect"
	AccountActionCallback  AccountAction = "callback"
	AccountActionSetKey    AccountAction = "set_key"
	AccountActionComplete  AccountAction = "complete"
	AccountActionRemove    AccountAction = "remove"
	AccountActionSetLimits AccountAction = "set_limits"
)

// AccountsActionArgs contains only the action kind, never account secrets.
type AccountsActionArgs struct {
	Action  AccountAction `json:"action"`
	Account AccountID     `json:"account,omitempty"`
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
	AccountUnknown       AccountState = "unknown"
	AccountFailedState   AccountState = "failed"
)

type CatalogModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AccountProviderInfo struct {
	// TODO: legacy/shim remove later — connectMethod and connectFlow; catalog PR 4 removes them.
	ConnectMethod ConnectMethod   `json:"connectMethod"`
	ConnectFlow   ConnectFlow     `json:"connectFlow"`
	ID            AccountProvider `json:"id"`
	DisplayName   string          `json:"displayName"`
	Flows         []ConnectFlow   `json:"flows"`
	KeyName       string          `json:"keyName,omitempty"`
	Models        []CatalogModel  `json:"models"`
}
type Account struct {
	Flow     ConnectFlow          `json:"flow"`
	ID       AccountID            `json:"id"`
	Provider AccountProvider      `json:"provider"`
	Label    string               `json:"label"`
	Rooms    []RoomID             `json:"rooms"`
	State    AccountState         `json:"state"`
	Reason   AccountFailureReason `json:"reason,omitempty"`
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
	ConnectFlowKey      ConnectFlow = "key"
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
type AccountsReconnectRequest struct {
	AccountID AccountID `json:"accountId"`
}

type AccountsConnectResponse struct {
	Attempt   string      `json:"attempt"`
	Flow      ConnectFlow `json:"flow"`
	Link      string      `json:"link"`
	Code      string      `json:"code,omitempty"`
	ExpiresAt time.Time   `json:"expiresAt"`
}
type AccountsReconnectResponse struct {
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
type ConnectFailureReason string

const (
	ConnectAdoptionFailed ConnectFailureReason = "adoption_failed"
	ConnectWrongAccount   ConnectFailureReason = "wrong_account"
)

type AccountsConnectStatusResponse struct {
	Status    ConnectStatus        `json:"status"`
	AccountID AccountID            `json:"accountId,omitempty"`
	Reason    ConnectFailureReason `json:"reason,omitempty"`
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
	Billing        ModelBilling  `json:"billing,omitempty"`
	Description    string        `json:"description"`
	Taste          int           `json:"taste"`
	Diagnostics    int           `json:"diagnostics"`
	Intelligence   int           `json:"intelligence"`
	Cost           int           `json:"cost"`
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
	Create  bool    `json:"create"`
	Profile Profile `json:"profile"`
}
type ProfileSetResponse struct{}
type ProfileRemoveRequest struct {
	Name ProfileName `json:"name"`
}
type ProfileRemoveResponse struct{}
type AccountFailureReason string

const (
	AccountSignInRejected    AccountFailureReason = "sign_in_rejected"
	AccountRefreshFailed     AccountFailureReason = "refresh_failed"
	AccountProxyFailed       AccountFailureReason = "proxy_failed"
	AccountCredentialMissing AccountFailureReason = "credential_missing"
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
