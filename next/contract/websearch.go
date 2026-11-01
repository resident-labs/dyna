package contract

type WebSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}
type WebSearchResult struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet"`
	Provider string `json:"provider"`
}
type WebSearchAttemptReason string

const (
	WebSearchNotSignedIn   WebSearchAttemptReason = "not_signed_in"
	WebSearchNoQuota       WebSearchAttemptReason = "no_quota"
	WebSearchRecentFailure WebSearchAttemptReason = "recent_failure"
	WebSearchFailed        WebSearchAttemptReason = "failed"
)

type WebSearchAttempt struct {
	Provider string                 `json:"provider"`
	Reason   WebSearchAttemptReason `json:"reason"`
	Message  string                 `json:"message,omitempty"`
}

type WebSearchResponse struct {
	Results  []WebSearchResult  `json:"results"`
	Provider string             `json:"provider"`
	Attempts []WebSearchAttempt `json:"attempts"`
}
type WebsearchSetKeyRequest struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}
type WebsearchSetKeyResponse struct{}
type WebsearchRemoveKeyRequest struct {
	Provider string `json:"provider"`
}
type WebsearchRemoveKeyResponse struct{}
