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
type WebSearchResponse struct {
	Results []WebSearchResult `json:"results"`
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
