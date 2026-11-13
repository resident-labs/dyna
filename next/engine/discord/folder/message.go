package folder

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseMessage(path string, data []byte) (Message, error) {
	message := Message{Mentions: "none", Source: Source{File: path, BodyLine: 1}}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		message.Body = text
		return message, nil
	}
	header, body, found := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
	if !found {
		return message, fmt.Errorf("%s: line 1: unclosed front matter", path)
	}
	var metadata struct {
		Title    string `yaml:"title"`
		Color    string `yaml:"color"`
		Footer   string `yaml:"footer"`
		Pin      bool   `yaml:"pin"`
		Mentions string `yaml:"mentions"`
		Embed    string `yaml:"embed"`
	}
	if err := Decode(path, []byte(header), &metadata); err != nil {
		return message, err
	}
	if metadata.Mentions != "" && metadata.Mentions != "none" {
		return message, fmt.Errorf("%s: mentions must be none", path)
	}
	message.Title, message.Color, message.Footer, message.Pin = metadata.Title, metadata.Color, metadata.Footer, metadata.Pin
	if metadata.Embed != "" {
		if !json.Valid([]byte(metadata.Embed)) {
			return message, fmt.Errorf("%s: embed must contain valid JSON", path)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(metadata.Embed), &object); err != nil || object == nil {
			return message, fmt.Errorf("%s: embed must be a JSON object", path)
		}
		message.Embed = json.RawMessage(metadata.Embed)
	}
	message.Body = body
	message.Source.BodyLine = strings.Count(header, "\n") + 4
	return message, nil
}
