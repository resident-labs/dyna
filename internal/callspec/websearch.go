package callspec

import "dyna-agent/internal/websearch"

// WebSearchRequest is one unified web search: the query text and how many
// results to return, where zero means the default.
type WebSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// WebSearchResponse carries the results of one search, in the order the
// provider ranked them.
type WebSearchResponse struct {
	Results []websearch.Result `json:"results"`
}
