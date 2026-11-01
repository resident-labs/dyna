package contract

import "time"

// LocalClient names the owner's own harnesses on the model route. Session IDs
// start with ses_, so it is never a session.
const LocalClient = "local"

// ProxyKeyRequest reads the owner's proxy key, making it on first use. Reset replaces it.
type ProxyKeyRequest struct {
	Reset bool `json:"reset,omitempty"`
}
type ProxyKeyResponse struct {
	Key string `json:"key"`
}

type ProxyUsageRequest struct{}

// ProxyUsage totals one model's completed requests from the local client.
type ProxyUsage struct {
	Model            string    `json:"model"`
	Requests         int64     `json:"requests"`
	InputTokens      int64     `json:"inputTokens"`
	OutputTokens     int64     `json:"outputTokens"`
	CacheReadTokens  int64     `json:"cacheReadTokens"`
	CacheWriteTokens int64     `json:"cacheWriteTokens"`
	LastAt           time.Time `json:"lastAt"`
}
type ProxyUsageResponse struct {
	Models []ProxyUsage `json:"models"`
}
