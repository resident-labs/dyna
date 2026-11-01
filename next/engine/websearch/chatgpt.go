package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// ChatGPTModel is the model that runs the search. The engine passes its own
	// choice in ChatGPT.Model.
	//
	// Only a model that the Codex client itself serves runs on a ChatGPT
	// account: any other id makes the backend answer HTTP 400 model_not_found.
	// This is the current Codex model list (codex 0.164.0) that the backend
	// accepted for a search on the owner's sign-in; gpt-5-codex, which this tool
	// sent first, is no longer among them.
	ChatGPTModel = "gpt-5.6-luna"
	// chatGPTInstructions is the system prompt the Codex backend requires. The
	// installed Pi Codex client always sends one, and a request without it is
	// refused, so it is a constant rather than an option.
	chatGPTInstructions = "You are Dyna's web search tool. Run one web search for the request and answer in a few sentences from the sources it returns."
)

// ChatGPT asks the ChatGPT web search tool and maps its cited sources to
// results. The model runs the search itself and answers with citations.
type ChatGPT struct {
	// BaseURL is the proxy API root.
	BaseURL string
	// Model runs the search; empty uses ChatGPTModel.
	Model string
	// Client sends requests through the model proxy.
	Client *http.Client
}

// Name is "chatgpt".
func (c ChatGPT) Name() string { return "chatgpt" }

// Search asks the ChatGPT search tool about q.
func (c ChatGPT) Search(ctx context.Context, q Query) ([]Result, error) {
	if c.Client == nil || c.BaseURL == "" {
		return nil, ErrNotSignedIn
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	base := c.BaseURL
	model := c.Model
	if model == "" {
		model = ChatGPTModel
	}
	body, err := json.Marshal(chatGPTSearch(model, q.Text))
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := c.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("chatgpt request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError("chatgpt", response.StatusCode, response.Body)
	}
	stream, err := readResponsesStream(response.Body, "chatgpt")
	if err != nil {
		return nil, err
	}
	if stream.failure != nil {
		return nil, stream.failure
	}
	return capped(c.Name(), q, stream.chatGPTResults()), nil
}

// chatGPTRequest is the body of a search request. It stores nothing and
// streams the answer.
func chatGPTSearch(model, query string) chatGPTRequest {
	prompt := "Search the web for: " + query +
		"\n\nUse the web search tool. Answer in at most six sentences and cite every source you use."
	return chatGPTRequest{
		Model:        model,
		Instructions: chatGPTInstructions,
		Input:        []chatGPTTurn{{Role: "user", Content: []chatGPTPart{{Type: "input_text", Text: prompt}}}},
		Tools: []chatGPTTool{{
			Type: "web_search",
			// The ChatGPT backend searches its index by default. A search tool
			// asks the live web instead, as the Codex CLI does for a live search.
			ExternalWebAccess: true,
		}},
		Store:  false,
		Stream: true,
	}
}

type chatGPTRequest struct {
	Model string `json:"model"`
	// Instructions is the system prompt; the backend refuses a request without
	// one.
	Instructions string        `json:"instructions"`
	Input        []chatGPTTurn `json:"input"`
	Tools        []chatGPTTool `json:"tools"`
	Store        bool          `json:"store"`
	Stream       bool          `json:"stream"`
}

type chatGPTTurn struct {
	Role    string        `json:"role"`
	Content []chatGPTPart `json:"content"`
}

type chatGPTPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type chatGPTTool struct {
	Type string `json:"type"`
	// ExternalWebAccess asks for the live web rather than the cached index.
	ExternalWebAccess bool `json:"external_web_access"`
}

// results maps the citations to results, each with the answer span that cites
// it. One URL appears once, in the order the answer cites it.
func (s *responsesStream) chatGPTResults() []Result {
	answer := s.answer.String()
	if strings.TrimSpace(answer) == "" {
		answer = strings.Join(s.texts, "\n\n")
	}
	seen := map[string]bool{}
	results := make([]Result, 0, len(s.cites))
	for _, cite := range s.cites {
		if cite.URL == "" || seen[cite.URL] {
			continue
		}
		seen[cite.URL] = true
		snippet := citedText(answer, cite)
		if snippet == "" {
			snippet = text(answer)
		}
		results = append(results, Result{Title: text(cite.Title), URL: cite.URL, Snippet: snippet})
	}
	return results
}

// citedText is the answer span a citation marks, or empty when the span does
// not fit the answer.
func citedText(answer string, cite responsesCitation) string {
	runes := []rune(answer)
	if cite.StartIndex < 0 || cite.EndIndex <= cite.StartIndex || cite.EndIndex > len(runes) {
		return ""
	}
	return text(string(runes[cite.StartIndex:cite.EndIndex]))
}
