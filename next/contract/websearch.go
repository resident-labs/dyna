package contract

type WebSearchProvider string

const (
	WebSearchChatGPT WebSearchProvider = "chatgpt"
	WebSearchClaude  WebSearchProvider = "claude"
	WebSearchMuse    WebSearchProvider = "muse"
	WebSearchKimi    WebSearchProvider = "kimi"
	WebSearchZAI     WebSearchProvider = "zai"
	WebSearchXAI     WebSearchProvider = "xai"
	WebSearchBrave   WebSearchProvider = "brave"
	WebSearchExa     WebSearchProvider = "exa"
	WebSearchTavily  WebSearchProvider = "tavily"
)

type WebSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}
type WebSearchResult struct {
	Title    string            `json:"title"`
	URL      string            `json:"url"`
	Snippet  string            `json:"snippet"`
	Provider WebSearchProvider `json:"provider"`
}
type WebSearchAttemptReason string

const (
	WebSearchNotSignedIn   WebSearchAttemptReason = "not_signed_in"
	WebSearchNoQuota       WebSearchAttemptReason = "no_quota"
	WebSearchRecentFailure WebSearchAttemptReason = "recent_failure"
	WebSearchFailed        WebSearchAttemptReason = "failed"
)

type WebSearchAttempt struct {
	Provider WebSearchProvider      `json:"provider"`
	Reason   WebSearchAttemptReason `json:"reason"`
	Message  string                 `json:"message,omitempty"`
}
type WebSearchResponse struct {
	Results  []WebSearchResult  `json:"results"`
	Provider WebSearchProvider  `json:"provider"`
	Attempts []WebSearchAttempt `json:"attempts"`
}
type WebsearchSetKeyRequest struct {
	Provider WebSearchProvider `json:"provider"`
	Key      string            `json:"key"`
}
type WebsearchSetKeyResponse struct{}
type WebsearchRemoveKeyRequest struct {
	Provider WebSearchProvider `json:"provider"`
}
type WebsearchRemoveKeyResponse struct{}
