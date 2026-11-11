package discord

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// History is best effort: a single read, newest first from Discord, rendered oldest first.
func (c *Client) History(ctx context.Context, channel, before, bot string, owner ...string) string {
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
		if m.Author.ID == bot {
			break
		}
		if len(lines) == 20 {
			break
		}
		author := "@" + m.Author.Username
		if len(owner) > 0 && owner[0] != "" && m.Author.ID == owner[0] {
			author = "owner"
		}
		line := []rune(author + ": " + m.Content)
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
