package websearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ChatGPTEndpoint is the ChatGPT backend the Codex CLI uses.
	ChatGPTEndpoint = "https://chatgpt.com/backend-api/codex"
	// ChatGPTModel is the model that runs the search. The engine passes its own
	// choice in ChatGPT.Model.
	ChatGPTModel = "gpt-5-codex"
	// chatGPTInstructions is the system prompt the Codex backend requires. The
	// installed Pi Codex client always sends one, and a request without it is
	// refused, so it is a constant rather than an option.
	chatGPTInstructions = "You are Dyna's web search tool. Run one web search for the request and answer in a few sentences from the sources it returns."
	// chatGPTSource is the header value OpenAI expects from a Codex sign-in.
	chatGPTSource = "codex_cli_rs"
	// chatGPTMaxStream caps one answer stream.
	chatGPTMaxStream = 8 << 20
)

// chatGPTClient is how long one ChatGPT answer may take.
var chatGPTClient = &http.Client{Timeout: 90 * time.Second}

// Credential gives the access token and account ID of a ChatGPT sign-in. The
// engine reads the active Pi ChatGPT login, so a renewal between two searches
// takes effect at the next one.
type Credential interface {
	Token(ctx context.Context) (access, account string, err error)
}

// ChatGPT asks the ChatGPT web search tool and maps its cited sources to
// results. The model runs the search itself and answers with citations.
type ChatGPT struct {
	// BaseURL is the API root; empty uses ChatGPTEndpoint.
	BaseURL string
	// Model runs the search; empty uses ChatGPTModel.
	Model string
	// Credential reads the sign-in, or returns ErrNotSignedIn when there is
	// none.
	Credential Credential
	// Client sends the request; nil uses a client with a 90 s timeout.
	Client *http.Client
}

// Name is "chatgpt".
func (c ChatGPT) Name() string { return "chatgpt" }

// Search asks the ChatGPT search tool about q.
func (c ChatGPT) Search(ctx context.Context, q Query) ([]Result, error) {
	if c.Credential == nil {
		return nil, ErrNotSignedIn
	}
	access, account, err := c.Credential.Token(ctx)
	if err != nil {
		return nil, err
	}
	if access == "" || account == "" {
		return nil, ErrNotSignedIn
	}
	base := c.BaseURL
	if base == "" {
		base = ChatGPTEndpoint
	}
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
	request.Header.Set("Authorization", "Bearer "+access)
	request.Header.Set("ChatGPT-Account-ID", account)
	request.Header.Set("originator", chatGPTSource)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	client := c.Client
	if client == nil {
		client = chatGPTClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("chatgpt request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return nil, statusError("chatgpt", response.StatusCode)
	}
	stream, err := readChatGPTStream(response.Body)
	if err != nil {
		return nil, err
	}
	if stream.failure != nil {
		return nil, stream.failure
	}
	return capped(c.Name(), q, stream.results()), nil
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

// chatGPTEvent is one server-sent event of the answer.
type chatGPTEvent struct {
	Type       string         `json:"type"`
	Delta      string         `json:"delta"`
	Message    string         `json:"message"`
	Item       *chatGPTItem   `json:"item"`
	Annotation *chatGPTCite   `json:"annotation"`
	Response   *chatGPTAnswer `json:"response"`
}

type chatGPTAnswer struct {
	Output []chatGPTItem  `json:"output"`
	Error  *chatGPTFailed `json:"error"`
}

// chatGPTFailed is why the backend refused the answer.
type chatGPTFailed struct {
	Message string `json:"message"`
}

type chatGPTItem struct {
	Type    string         `json:"type"`
	Content []chatGPTPiece `json:"content"`
}

type chatGPTPiece struct {
	Type        string        `json:"type"`
	Text        string        `json:"text"`
	Annotations []chatGPTCite `json:"annotations"`
}

// chatGPTCite is one url_citation annotation of an answer.
type chatGPTCite struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
}

// chatGPTStream is what one answer stream said: its text, its citations, and
// the failure it reported, if any.
type chatGPTStream struct {
	answer  strings.Builder
	texts   []string
	cites   []chatGPTCite
	failure error
}

// readChatGPTStream reads the answer stream to its end.
func readChatGPTStream(r io.Reader) (*chatGPTStream, error) {
	stream := &chatGPTStream{}
	scanner := bufio.NewScanner(io.LimitReader(r, chatGPTMaxStream))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			break
		}
		var event chatGPTEvent
		if json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		stream.add(event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("chatgpt answer stopped early: %w", err)
	}
	return stream, nil
}

// add records one event's text and citations.
func (s *chatGPTStream) add(event chatGPTEvent) {
	if event.Type == "error" {
		s.failure = fmt.Errorf("chatgpt reported an error: %s", text(event.Message))
		return
	}
	if event.Response != nil && event.Response.Error != nil {
		s.failure = fmt.Errorf("chatgpt reported an error: %s", text(event.Response.Error.Message))
		return
	}
	s.answer.WriteString(event.Delta)
	if event.Annotation != nil && event.Annotation.Type == "url_citation" {
		s.cites = append(s.cites, *event.Annotation)
	}
	if event.Item != nil {
		s.addItem(*event.Item)
	}
	if event.Response != nil {
		for _, item := range event.Response.Output {
			s.addItem(item)
		}
	}
}

func (s *chatGPTStream) addItem(item chatGPTItem) {
	for _, piece := range item.Content {
		if piece.Text != "" {
			s.texts = append(s.texts, piece.Text)
		}
		for _, cite := range piece.Annotations {
			if cite.Type == "url_citation" {
				s.cites = append(s.cites, cite)
			}
		}
	}
}

// results maps the citations to results, each with the answer span that cites
// it. One URL appears once, in the order the answer cites it.
func (s *chatGPTStream) results() []Result {
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
func citedText(answer string, cite chatGPTCite) string {
	runes := []rune(answer)
	if cite.StartIndex < 0 || cite.EndIndex <= cite.StartIndex || cite.EndIndex > len(runes) {
		return ""
	}
	return text(string(runes[cite.StartIndex:cite.EndIndex]))
}
