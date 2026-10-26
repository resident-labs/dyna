package websearch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// XAIEndpoint is the proxy the Grok Build CLI calls for a model turn.
	XAIEndpoint = "https://cli-chat-proxy.grok.com/v1"
	// XAIModel is the web search model xAI runs a search with. The engine
	// passes its own choice in XAI.Model.
	XAIModel = "grok-4.6"
	// xaiTokenAuth is the header value that marks a Grok CLI sign-in.
	xaiTokenAuth = "xai-grok-cli"
	// xaiClientIdentifier, xaiClientMode and xaiClientVersion are the client
	// name the Grok Build 1.0.67 binary sends with a model turn.
	xaiClientIdentifier = "cli-chat-proxy"
	xaiClientMode       = "chat-proxy"
	xaiClientVersion    = "1.0.67"
	// xaiMaxStream caps one answer stream.
	xaiMaxStream = 8 << 20
)

// xaiClient is how long one xAI answer may take.
var xaiClient = &http.Client{Timeout: 90 * time.Second}

// XAI searches the web with xAI's hosted web search tool. Grok has no search
// endpoint of its own: the CLI runs a Responses turn with the web_search tool
// and reads the cited sources, so this provider makes the same call with the
// same saved sign-in.
type XAI struct {
	// BaseURL is the API root; empty uses XAIEndpoint.
	BaseURL string
	// Model is the web search model; empty uses XAIModel.
	Model string
	// Token reads the saved Grok sign-in token, or returns ErrNotSignedIn when
	// there is none.
	Token func(ctx context.Context) (string, error)
	// Client sends the request; nil uses a client with a 90 s timeout.
	Client *http.Client
}

// Name is "xai".
func (x XAI) Name() string { return "xai" }

// Search asks xAI to search the web for q.
func (x XAI) Search(ctx context.Context, q Query) ([]Result, error) {
	if x.Token == nil {
		return nil, ErrNotSignedIn
	}
	token, err := x.Token(ctx)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, ErrNotSignedIn
	}
	base := x.BaseURL
	if base == "" {
		base = XAIEndpoint
	}
	model := x.Model
	if model == "" {
		model = XAIModel
	}
	body, err := json.Marshal(xaiSearch(model, q.Text))
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Xai-Token-Auth", xaiTokenAuth)
	request.Header.Set("X-Grok-Model-Override", model)
	request.Header.Set("X-Grok-Client-Identifier", xaiClientIdentifier)
	request.Header.Set("X-Grok-Client-Mode", xaiClientMode)
	request.Header.Set("X-Grok-Client-Version", xaiClientVersion)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	client := x.Client
	if client == nil {
		client = xaiClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("xai request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError("xai", response.StatusCode, response.Body, token)
	}
	stream, err := readXAIStream(response.Body)
	if err != nil {
		return nil, err
	}
	if stream.failure != nil {
		return nil, stream.failure
	}
	return capped(x.Name(), q, stream.results()), nil
}

// xaiSearch is the body of one search turn: it stores nothing, streams the
// answer, and asks for the sources the search call used.
func xaiSearch(model, query string) xaiRequest {
	prompt := "Search the web for: " + query +
		"\n\nUse the web search tool. Answer in at most six sentences and cite every source you use."
	return xaiRequest{
		Model:   model,
		Input:   []xaiTurn{{Role: "user", Content: []xaiPart{{Type: "input_text", Text: prompt}}}},
		Tools:   []xaiTool{{Type: "web_search"}},
		Include: []string{"web_search_call.action.sources"},
		Store:   false,
		Stream:  true,
	}
}

type xaiRequest struct {
	Model   string    `json:"model"`
	Input   []xaiTurn `json:"input"`
	Tools   []xaiTool `json:"tools"`
	Include []string  `json:"include"`
	Store   bool      `json:"store"`
	Stream  bool      `json:"stream"`
}

type xaiTurn struct {
	Role    string    `json:"role"`
	Content []xaiPart `json:"content"`
}

type xaiPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type xaiTool struct {
	Type string `json:"type"`
}

// xaiEvent is one server-sent event of the answer.
type xaiEvent struct {
	Type       string       `json:"type"`
	Delta      string       `json:"delta"`
	Message    string       `json:"message"`
	Item       *xaiItem     `json:"item"`
	Annotation *xaiCitation `json:"annotation"`
	Response   *xaiAnswer   `json:"response"`
}

type xaiAnswer struct {
	Output []xaiItem   `json:"output"`
	Error  *xaiFailure `json:"error"`
}

// xaiFailure is why the backend refused the answer.
type xaiFailure struct {
	Message string `json:"message"`
}

type xaiItem struct {
	Type    string         `json:"type"`
	Content []xaiPiece     `json:"content"`
	Action  *xaiSearchCall `json:"action"`
}

type xaiPiece struct {
	Type        string        `json:"type"`
	Text        string        `json:"text"`
	Annotations []xaiCitation `json:"annotations"`
}

// xaiSearchCall is the web search a turn ran, with the sources it read.
type xaiSearchCall struct {
	Sources []xaiSource `json:"sources"`
}

// xaiSource is one source of a search call. It carries no snippet.
type xaiSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// xaiCitation is one url_citation annotation of an answer.
type xaiCitation struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
}

// xaiStream is what one answer stream said: its text, its citations, the
// sources of its search call, and the failure it reported, if any.
type xaiStream struct {
	answer  strings.Builder
	texts   []string
	cites   []xaiCitation
	sources []xaiSource
	failure error
}

// readXAIStream reads the answer stream to its end.
func readXAIStream(r io.Reader) (*xaiStream, error) {
	stream := &xaiStream{}
	scanner := bufio.NewScanner(io.LimitReader(r, xaiMaxStream))
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
		var event xaiEvent
		if json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		stream.add(event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("xai answer stopped early: %w", err)
	}
	return stream, nil
}

// add records one event's text, citations and sources.
func (s *xaiStream) add(event xaiEvent) {
	if event.Type == "error" {
		s.failure = fmt.Errorf("xai reported an error: %s", text(event.Message))
		return
	}
	if event.Response != nil && event.Response.Error != nil {
		s.failure = fmt.Errorf("xai reported an error: %s", text(event.Response.Error.Message))
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

func (s *xaiStream) addItem(item xaiItem) {
	if item.Action != nil {
		s.sources = append(s.sources, item.Action.Sources...)
	}
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

// results maps the citations and the search sources to results. A citation
// comes first with the answer span that cites it; a source that the answer did
// not cite follows with its title only. One URL appears once.
func (s *xaiStream) results() []Result {
	answer := s.answer.String()
	if strings.TrimSpace(answer) == "" {
		answer = strings.Join(s.texts, "\n\n")
	}
	seen := map[string]bool{}
	results := make([]Result, 0, len(s.cites)+len(s.sources))
	for _, cite := range s.cites {
		if cite.URL == "" || seen[cite.URL] {
			continue
		}
		seen[cite.URL] = true
		results = append(results, Result{Title: xaiTitle(cite, s.sources), URL: cite.URL, Snippet: xaiCitedText(answer, cite)})
	}
	for _, source := range s.sources {
		if source.URL == "" || seen[source.URL] {
			continue
		}
		seen[source.URL] = true
		results = append(results, Result{Title: text(source.Title), URL: source.URL})
	}
	return results
}

// xaiCitedText is the answer sentence a citation supports. xAI's offsets mark
// the citation marker itself, such as "[[1]](https://example.com)", and not the
// sentence that cites it, so the snippet is the sentence around that marker with
// the marker removed.
func xaiCitedText(answer string, cite xaiCitation) string {
	runes := []rune(answer)
	if cite.StartIndex < 0 || cite.StartIndex > len(runes) || cite.EndIndex < cite.StartIndex || cite.EndIndex > len(runes) {
		return ""
	}
	sentence := string(runes[xaiSentenceStart(runes, cite.StartIndex):xaiSentenceEnd(runes, cite.EndIndex)])
	sentence = xaiMarkers.ReplaceAllString(sentence, "")
	return text(xaiSpacing.ReplaceAllString(sentence, "$1"))
}

// xaiMarkers are the citation links xAI writes into an answer: the numeric
// label in brackets, then the source URL in parentheses.
var xaiMarkers = regexp.MustCompile(`\[\[?\d+\]?\]\([^)]*\)`)

// xaiSpacing is the gap a removed marker leaves before punctuation.
var xaiSpacing = regexp.MustCompile(`\s+([.,!?;:])`)

// xaiSentenceStart is where the sentence that holds a span begins.
func xaiSentenceStart(runes []rune, at int) int {
	for i := at - 1; i >= 0; i-- {
		if strings.ContainsRune(".!?\n", runes[i]) {
			return i + 1
		}
	}
	return 0
}

// xaiSentenceEnd is where the sentence that holds a span ends, after its own
// terminator.
func xaiSentenceEnd(runes []rune, at int) int {
	for i := at; i < len(runes); i++ {
		if strings.ContainsRune(".!?\n", runes[i]) {
			return i + 1
		}
	}
	return len(runes)
}

// xaiTitle names a citation. xAI titles a citation with its numeric label, so a
// bare label falls back to the search call's title for that URL and then to the
// URL itself.
func xaiTitle(cite xaiCitation, sources []xaiSource) string {
	if title := text(cite.Title); title != "" && !xaiLabel(title) {
		return title
	}
	for _, source := range sources {
		if source.URL == cite.URL && strings.TrimSpace(source.Title) != "" {
			return text(source.Title)
		}
	}
	return cite.URL
}

// xaiLabel reports a citation title that is only the numeric label.
func xaiLabel(title string) bool {
	for _, r := range title {
		if r < '0' || r > '9' {
			return false
		}
	}
	return title != ""
}
