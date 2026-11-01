package websearch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const responsesMaxStream = 8 << 20

// responsesEvent is one server-sent event of the answer.
type responsesEvent struct {
	Type       string             `json:"type"`
	Delta      string             `json:"delta"`
	Message    string             `json:"message"`
	Item       *responsesItem     `json:"item"`
	Annotation *responsesCitation `json:"annotation"`
	Response   *responsesAnswer   `json:"response"`
}

type responsesAnswer struct {
	Output []responsesItem   `json:"output"`
	Error  *responsesFailure `json:"error"`
}

// responsesFailure is why the backend refused the answer.
type responsesFailure struct {
	Message string `json:"message"`
}

type responsesItem struct {
	Type    string               `json:"type"`
	Content []responsesPiece     `json:"content"`
	Action  *responsesSearchCall `json:"action"`
}

type responsesPiece struct {
	Type        string              `json:"type"`
	Text        string              `json:"text"`
	Annotations []responsesCitation `json:"annotations"`
}

// responsesSearchCall is the web search a turn ran, with the sources it read.
type responsesSearchCall struct {
	Sources []responsesSource `json:"sources"`
}

// responsesSource is one source of a search call. It carries no snippet.
type responsesSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// responsesCitation is one url_citation annotation of an answer.
type responsesCitation struct {
	Type       string `json:"type"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
}

// responsesStream is what one answer stream said: its text, its citations, the
// sources of its search call, and the failure it reported, if any.
type responsesStream struct {
	answer  strings.Builder
	texts   []string
	cites   []responsesCitation
	sources []responsesSource
	failure error
}

// readResponsesStream reads the answer stream to its end.
func readResponsesStream(r io.Reader, provider string) (*responsesStream, error) {
	stream := &responsesStream{}
	scanner := bufio.NewScanner(io.LimitReader(r, responsesMaxStream))
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
		var event responsesEvent
		if json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		stream.add(event, provider)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s answer stopped early: %w", provider, err)
	}
	return stream, nil
}

// add records one event's text, citations and sources.
func (s *responsesStream) add(event responsesEvent, provider string) {
	if event.Type == "error" {
		s.failure = fmt.Errorf("%s reported an error: %s", provider, text(redact(event.Message)))
		return
	}
	if event.Response != nil && event.Response.Error != nil {
		s.failure = fmt.Errorf("%s reported an error: %s", provider, text(redact(event.Response.Error.Message)))
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

func (s *responsesStream) addItem(item responsesItem) {
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
