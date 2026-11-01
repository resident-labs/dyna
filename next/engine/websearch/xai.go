package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// XAIModel is the web search model xAI runs a search with. The engine
	// passes its own choice in XAI.Model.
	XAIModel = "grok-4.6"
)

// XAI searches the web with xAI's hosted web search tool. Grok has no search
// endpoint of its own: the CLI runs a Responses turn with the web_search tool
// and reads the cited sources, so this provider makes the same call with the
// same saved sign-in.
type XAI struct {
	// BaseURL is the proxy API root.
	BaseURL string
	// Model is the web search model; empty uses XAIModel.
	Model string
	// Client sends requests through the model proxy.
	Client *http.Client
}

// Name is "xai".
func (x XAI) Name() string { return "xai" }

// Search asks xAI to search the web for q.
func (x XAI) Search(ctx context.Context, q Query) ([]Result, error) {
	if x.Client == nil || x.BaseURL == "" {
		return nil, ErrNotSignedIn
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	base := x.BaseURL
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
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := x.Client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("xai request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError("xai", response.StatusCode, response.Body)
	}
	stream, err := readResponsesStream(response.Body, "xai")
	if err != nil {
		return nil, err
	}
	if stream.failure != nil {
		return nil, stream.failure
	}
	return capped(x.Name(), q, stream.xaiResults()), nil
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

// results maps the citations and the search sources to results. A citation
// comes first with the answer span that cites it; a source that the answer did
// not cite follows with its title only. One URL appears once.
func (s *responsesStream) xaiResults() []Result {
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

// xaiCitedText is the prose a citation supports. xAI writes a citation after
// its sentence's terminator, often with several citations in a row, so the
// snippet is the sentence before the citation. Each marker is blanked first:
// a marker holds a URL, and a URL's dots would look like sentence ends.
func xaiCitedText(answer string, cite responsesCitation) string {
	runes := []rune(answer)
	if cite.StartIndex < 0 || cite.StartIndex > len(runes) || cite.EndIndex < cite.StartIndex {
		return ""
	}
	prose := xaiBlankMarkers(runes)
	end := cite.StartIndex
	for end > 0 && unicode.IsSpace(prose[end-1]) {
		end--
	}
	return text(string(prose[xaiSentenceStart(prose, end):end]))
}

// xaiMarkers are the citation links xAI writes into an answer: the numeric
// label in brackets, then the source URL in parentheses.
var xaiMarkers = regexp.MustCompile(`\[\[?\d+\]?\]\([^)]*\)`)

// xaiBlankMarkers replaces every citation marker with spaces. Each character
// keeps its place, so a citation's offset still points at the same position.
func xaiBlankMarkers(answer []rune) []rune {
	blanked := make([]rune, len(answer))
	copy(blanked, answer)
	prose := string(answer)
	for _, found := range xaiMarkers.FindAllStringIndex(prose, -1) {
		start := utf8.RuneCountInString(prose[:found[0]])
		stop := start + utf8.RuneCountInString(prose[found[0]:found[1]])
		for i := start; i < stop; i++ {
			blanked[i] = ' '
		}
	}
	return blanked
}

// xaiSentenceStart is where the sentence that ends at end begins. The
// terminator just before end belongs to that sentence, so the boundary is the
// terminator before it.
func xaiSentenceStart(prose []rune, end int) int {
	at := end - 1
	if at >= 0 && strings.ContainsRune(".!?\n", prose[at]) {
		at--
	}
	for i := at; i >= 0; i-- {
		if strings.ContainsRune(".!?\n", prose[i]) {
			return i + 1
		}
	}
	return 0
}

// xaiTitle names a citation. xAI titles a citation with its numeric label, so a
// bare label falls back to the search call's title for that URL and then to the
// URL itself.
func xaiTitle(cite responsesCitation, sources []responsesSource) string {
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
