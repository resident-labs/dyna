package discord

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// History is best effort: a single read, newest first from Discord, rendered oldest first. The bot's own messages are in
// it, cut short: one bot speaks for several sessions, and the one answering has not seen what another said.
func (c *Client) History(ctx context.Context, channel, before, bot string) string {
	r, err := c.request(ctx, "GET", "/api/v10/channels/"+url.PathEscape(channel)+"/messages?limit=20&before="+url.QueryEscape(before), nil, "application/json", "")
	if err != nil || r.Status != 200 {
		return ""
	}
	var messages []Message
	if json.Unmarshal(r.Body, &messages) != nil {
		return ""
	}
	var lines []string
	remaining := 4000
	for _, m := range messages {
		if len(lines) == 20 {
			break
		}
		text := m.Author.Username + ": " + m.Content
		for _, part := range m.Parts() {
			text += " [" + part + "]"
		}
		if files := m.Files(); len(files) > 0 {
			var names []string
			for _, f := range files {
				names = append(names, f.Name)
			}
			// The files are not delivered with history; the ID is what discord read needs to fetch them.
			text += " [files: " + strings.Join(names, ", ") + "; discord read with message " + m.ID + " delivers them]"
		}
		line := []rune(text)
		if m.Author.ID == bot && len(line) > 300 {
			line = append(line[:300], '…')
		}
		if len(lines) > 0 {
			remaining--
		}
		if remaining <= 0 {
			break
		}
		if len(line) > remaining {
			line = line[:remaining]
		}
		lines = append(lines, string(line))
		remaining -= len(line)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}
