package websearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ClaudeEndpoint is the Anthropic API root.
	ClaudeEndpoint = "https://api.anthropic.com"
	// ClaudeModel runs the search. The engine passes its own choice in
	// Claude.Model.
	ClaudeModel = "claude-sonnet-4-6"
	// claudeVersion is the API version every Anthropic request states.
	claudeVersion = "2023-06-01"
	// claudeBetas are the betas the Claude Code client sends for a search: its
	// own client beta, its OAuth sign-in beta, and the web search tool beta.
	claudeBetas = "claude-code-20250219,oauth-2025-04-20,web-search-2025-03-05"
	// claudeAgent is the client the saved sign-in belongs to. Anthropic binds
	// an OAuth token to its client, so the request states that client.
	claudeAgent = "claude-cli/2.1.309 (external, cli)"
	// claudeApp is the client app header of the same bundle.
	claudeApp = "cli"
	// claudeSearchTool is the server-side search tool the API runs.
	claudeSearchTool = "web_search_20250305"
	// claudeSearchName is the tool's name in a request and in its blocks.
	claudeSearchName = "web_search"
	// claudeMaxUses is how many searches one request may run.
	claudeMaxUses = 1
	// claudeMaxTokens caps the summary the model writes beside the hits.
	claudeMaxTokens = 1024
	// claudeMaxStream caps one answer stream.
	claudeMaxStream = 8 << 20
	// claudeInstructions is the system prompt the Claude Code search query
	// uses.
	claudeInstructions = "You run a web search for another Claude model, which is waiting on the result and can search again. " +
		"Search once, then reply with a brief summary of the results."
)

// claudeClient is how long one Claude answer may take.
var claudeClient = &http.Client{Timeout: 90 * time.Second}

// Claude searches with the Anthropic API and the owner's saved Claude sign-in.
// One request forces the server-side web search tool, which runs the search
// and returns its hits, so the provider never runs a CLI.
type Claude struct {
	// BaseURL is the API root; empty uses ClaudeEndpoint.
	BaseURL string
	// Model runs the search; empty uses ClaudeModel.
	Model string
	// Token reads the sign-in's access token, or returns ErrNotSignedIn when
	// the owner saved no Claude sign-in.
	Token func(ctx context.Context) (string, error)
	// Client sends the request; nil uses a client with a 90 s timeout.
	Client *http.Client
}

// Name is "claude".
func (c Claude) Name() string { return "claude" }

// Search asks the Claude web search tool about q.
func (c Claude) Search(ctx context.Context, q Query) ([]Result, error) {
	if c.Token == nil {
		return nil, ErrNotSignedIn
	}
	token, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNotSignedIn
	}
	base := c.BaseURL
	if base == "" {
		base = ClaudeEndpoint
	}
	model := c.Model
	if model == "" {
		model = ClaudeModel
	}
	body, err := json.Marshal(claudeSearch(model, q.Text))
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(base, "/")+"/v1/messages?beta=true", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("anthropic-version", claudeVersion)
	request.Header.Set("anthropic-beta", claudeBetas)
	request.Header.Set("User-Agent", claudeAgent)
	request.Header.Set("x-app", claudeApp)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	client := c.Client
	if client == nil {
		client = claudeClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("claude request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return nil, ErrNotSignedIn
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return nil, statusError("claude", response.StatusCode, response.Body, token)
	}
	answer, err := readClaudeStream(response.Body)
	if err != nil {
		return nil, err
	}
	if answer.failure != nil {
		return nil, answer.failure
	}
	results := answer.results()
	if len(results) == 0 {
		return nil, errors.New("claude returned no search results")
	}
	return capped(c.Name(), q, results), nil
}

// claudeSearch is the body of one forced search: the one tool, the one choice
// that runs it, and a streamed answer.
func claudeSearch(model, query string) claudeRequest {
	return claudeRequest{
		Model:     model,
		MaxTokens: claudeMaxTokens,
		System:    []claudeText{{Type: "text", Text: claudeInstructions}},
		Messages: []claudeMessage{{
			Role:    "user",
			Content: []claudeText{{Type: "text", Text: "Perform a web search for the query: " + query}},
		}},
		Tools:      []claudeTool{{Type: claudeSearchTool, Name: claudeSearchName, MaxUses: claudeMaxUses}},
		ToolChoice: claudeChoice{Type: "tool", Name: claudeSearchName},
		Stream:     true,
	}
}

type claudeRequest struct {
	Model      string          `json:"model"`
	MaxTokens  int             `json:"max_tokens"`
	System     []claudeText    `json:"system"`
	Messages   []claudeMessage `json:"messages"`
	Tools      []claudeTool    `json:"tools"`
	ToolChoice claudeChoice    `json:"tool_choice"`
	Stream     bool            `json:"stream"`
}

type claudeText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeMessage struct {
	Role    string       `json:"role"`
	Content []claudeText `json:"content"`
}

type claudeTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses"`
}

type claudeChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// claudeEvent is one server-sent event of the answer.
type claudeEvent struct {
	Type         string        `json:"type"`
	ContentBlock *claudeBlock  `json:"content_block"`
	Delta        *claudeDiff   `json:"delta"`
	Error        *claudeFailed `json:"error"`
}

// claudeBlock is a content block the stream starts: the tool call, the tool's
// result, or the summary text.
type claudeBlock struct {
	Type string `json:"type"`
	// ToolUseID ties a tool result to the tool call that ran it.
	ToolUseID string `json:"tool_use_id"`
	// Content is a tool result's hits, or its error object.
	Content json.RawMessage `json:"content"`
}

// claudeDiff is one piece of a growing block.
type claudeDiff struct {
	Type     string          `json:"type"`
	Citation *claudeCitation `json:"citation"`
}

// claudeCitation is a source the summary text cites.
type claudeCitation struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	CitedText string `json:"cited_text"`
}

// claudeHit is one page the search found.
type claudeHit struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// claudeSearchError is a search the API could not run.
type claudeSearchError struct {
	ErrorCode string `json:"error_code"`
}

// claudeFailed is why the API refused the request.
type claudeFailed struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// claudeAnswer is what one stream said: the pages it found, the citations that
// name them, and the failure it reported, if any.
type claudeAnswer struct {
	hits    []Result
	cited   map[string]claudeCitation
	failure error
}

// results keeps each URL once, in the order the search returned it, and gives a
// cited sentence to the pages that have one.
func (a *claudeAnswer) results() []Result {
	seen := make(map[string]bool, len(a.hits))
	unique := make([]Result, 0, len(a.hits))
	for _, hit := range a.hits {
		if hit.URL == "" || seen[hit.URL] {
			continue
		}
		seen[hit.URL] = true
		if citation, ok := a.cited[hit.URL]; ok {
			if hit.Title == "" {
				hit.Title = text(citation.Title)
			}
			if hit.Snippet == "" {
				hit.Snippet = text(citation.CitedText)
			}
		}
		if hit.Title == "" {
			hit.Title = hit.URL
		}
		unique = append(unique, hit)
	}
	return unique
}

// readClaudeStream reads the answer stream to its end.
func readClaudeStream(r io.Reader) (*claudeAnswer, error) {
	answer := &claudeAnswer{cited: map[string]claudeCitation{}}
	scanner := bufio.NewScanner(io.LimitReader(r, claudeMaxStream))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event claudeEvent
		if json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		answer.add(event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("claude answer stopped early: %w", err)
	}
	return answer, nil
}

// add records one event's hits, citations and failure.
func (a *claudeAnswer) add(event claudeEvent) {
	if event.Type == "error" && event.Error != nil {
		a.fail(event.Error.Message, "the API reported an error")
		return
	}
	if event.ContentBlock != nil {
		switch event.ContentBlock.Type {
		case "web_search_tool_result":
			a.addHits(event.ContentBlock.Content)
		}
	}
	if event.Delta != nil && event.Delta.Citation != nil && event.Delta.Type == "citations_delta" {
		citation := *event.Delta.Citation
		if citation.URL != "" {
			if _, seen := a.cited[citation.URL]; !seen {
				a.cited[citation.URL] = citation
			}
		}
	}
}

// addHits maps one tool result to pages, or records the search failure the
// result carries.
func (a *claudeAnswer) addHits(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var hits []claudeHit
	if json.Unmarshal(raw, &hits) == nil {
		for _, hit := range hits {
			if strings.TrimSpace(hit.URL) == "" {
				continue
			}
			a.hits = append(a.hits, Result{Title: text(hit.Title), URL: strings.TrimSpace(hit.URL)})
		}
		return
	}
	var failed claudeSearchError
	if json.Unmarshal(raw, &failed) == nil && failed.ErrorCode != "" {
		a.fail("the web search failed: "+failed.ErrorCode, "the web search failed")
	}
}

// fail keeps the first failure the stream reports, on one line.
func (a *claudeAnswer) fail(reason, fallback string) {
	if a.failure != nil {
		return
	}
	reason = text(reason)
	if reason == "" {
		reason = fallback
	}
	a.failure = errors.New("claude reported an error: " + reason)
}
