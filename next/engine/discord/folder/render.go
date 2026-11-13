package folder

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// A mention is "#channel" or "@Role", at the start of a line or after a space or
// "(". Names with spaces are written "#{name}" or "@{Core Team}". A name starts
// with a letter, so "#123" and "C#" stay plain text.
var mention = regexp.MustCompile(`(^|[\s(])([#@])(?:\{([^}\n]+)\}|(\p{L}[\p{L}\p{N}_-]*))`)

// IDLookup finds the Discord ID of a channel or role by its folder name.
type IDLookup func(name string) (id string, found bool)

// RenderMessage builds the JSON a standing message is posted with: one embed
// whose description is the text before the first "## Heading" and whose fields
// are the headings. "embed:" front matter replaces the generated embed. Mentions
// become Discord mentions but never ping. An unknown name refuses with file and line.
func RenderMessage(message Message, channelID, roleID IDLookup) (json.RawMessage, error) {
	embed := message.Embed
	if embed == nil {
		body, err := resolveMentions(message, channelID, roleID)
		if err != nil {
			return nil, err
		}
		embed, err = renderEmbed(message, body)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(map[string]any{"embeds": []json.RawMessage{embed}, "allowed_mentions": map[string][]string{"parse": {}}})
}

func resolveMentions(message Message, channelID, roleID IDLookup) (string, error) {
	var refusal error
	var result strings.Builder
	for index, line := range strings.Split(message.Body, "\n") {
		if index > 0 {
			result.WriteByte('\n')
		}
		result.WriteString(mention.ReplaceAllStringFunc(line, func(text string) string {
			parts := mention.FindStringSubmatch(text)
			prefix, sigil, name := parts[1], parts[2], parts[3]+parts[4]
			lookup, kind, format := channelID, "channel", "<#%s>"
			if sigil == "@" {
				lookup, kind, format = roleID, "role", "<@&%s>"
			}
			id, found := lookup(name)
			if !found && refusal == nil {
				refusal = fmt.Errorf("%s: line %d: unknown %s %s%s", message.Source.File, message.Source.BodyLine+index, kind, sigil, name)
			}
			return prefix + fmt.Sprintf(format, id)
		}))
	}
	return result.String(), refusal
}

func renderEmbed(message Message, body string) (json.RawMessage, error) {
	type field struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	embed := struct {
		Title       string            `json:"title,omitempty"`
		Description string            `json:"description,omitempty"`
		Color       int               `json:"color,omitempty"`
		Footer      map[string]string `json:"footer,omitempty"`
		Fields      []field           `json:"fields,omitempty"`
	}{Title: message.Title}
	parts := strings.Split("\n"+body, "\n## ")
	embed.Description = strings.TrimSpace(parts[0])
	for _, section := range parts[1:] {
		name, value, _ := strings.Cut(section, "\n")
		embed.Fields = append(embed.Fields, field{strings.TrimSpace(name), strings.TrimSpace(value)})
	}
	if message.Color != "" {
		color, err := strconv.ParseUint(strings.TrimPrefix(message.Color, "#"), 16, 24)
		if err != nil {
			return nil, fmt.Errorf("%s: color must be #RRGGBB", message.Source.File)
		}
		embed.Color = int(color)
	}
	if message.Footer != "" {
		embed.Footer = map[string]string{"text": message.Footer}
	}
	return json.Marshal(embed)
}
