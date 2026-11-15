package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
)

func (s *Service) DiscordRead(ctx context.Context, caller contract.Principal, req contract.DiscordReadRequest) (contract.DiscordReadResponse, *contract.Error) {
	empty := contract.DiscordReadResponse{}
	client, problem := s.discordClient(ctx, caller)
	if problem != nil {
		return empty, problem
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	if req.Limit < 1 || req.Limit > 100 {
		return empty, invalid("limit must be between 1 and 100")
	}
	cursors := 0
	query := url.Values{"limit": {strconv.Itoa(req.Limit)}}
	for key, value := range map[string]string{"before": req.Before, "after": req.After, "around": req.Around} {
		if value == "" {
			continue
		}
		if !discordID(value) {
			return empty, invalid("invalid message ID")
		}
		cursors++
		query.Set(key, value)
	}
	if cursors > 1 || (req.Message != "" && cursors > 0) {
		return empty, invalid("use only one of message, before, after or around")
	}
	if req.Message != "" && !discordID(req.Message) {
		return empty, invalid("invalid message ID")
	}
	scope, problem := s.discordReadScope(ctx, caller)
	if problem != nil {
		return empty, problem
	}
	channel := req.Channel
	if !discordID(channel) {
		name := strings.TrimPrefix(channel, "#")
		var candidates []string
		var matches []string
		for _, c := range s.discordDirectory.Channels() {
			if !scope.allowsChannel(c) {
				continue
			}
			candidates = append(candidates, fmt.Sprintf("#%s (%s)", c.Name, c.ID))
			if c.Name == name {
				matches = append(matches, c.ID)
			}
		}
		if len(matches) != 1 {
			return empty, invalid("channel name is unknown or ambiguous; use an ID. Available channels: " + contract.Outside("discord directory", "", strings.Join(candidates, ", ")))
		}
		channel = matches[0]
	}
	path := "/api/v10/channels/" + channel + "/messages"
	if req.Message != "" {
		path += "/" + req.Message
	} else {
		path += "?" + query.Encode()
	}
	resolved, ok := scope.channel(&s.discordDirectory, channel)
	if !ok || !scope.allowsChannel(resolved) {
		return empty, forbidden()
	}
	result, err := client.ReadMessages(ctx, path)
	if err != nil {
		return empty, failure(err)
	}
	if result.Status != 200 {
		return empty, invalid(fmt.Sprintf("Discord returned HTTP %d", result.Status))
	}
	var messages []struct {
		discord.Message
		Timestamp time.Time `json:"timestamp"`
		Reference struct {
			Message string `json:"message_id"`
		} `json:"message_reference"`
	}
	if req.Message != "" {
		result.Body = append(append([]byte{'['}, result.Body...), ']')
	}
	if err := json.Unmarshal(result.Body, &messages); err != nil {
		return empty, failure(err)
	}
	sort.SliceStable(messages, func(i, j int) bool {
		a, b := messages[i], messages[j]
		if !a.Timestamp.Equal(b.Timestamp) {
			return a.Timestamp.Before(b.Timestamp)
		}
		if len(a.ID) != len(b.ID) {
			return len(a.ID) < len(b.ID)
		}
		return a.ID < b.ID
	})
	var fileStatus string
	if req.Message != "" && len(messages) == 1 {
		if messages[0].Channel == "" {
			messages[0].Channel = channel
		}
		fileStatus, problem = s.discordReadFiles(ctx, caller, messages[0].Message)
		if problem != nil {
			return empty, problem
		}
	}
	var lines []string
	for _, m := range messages {
		display := m.Member.Nick
		if display == "" {
			display = m.Author.GlobalName
		}
		if display == "" {
			display = m.Author.Username
		}
		line := fmt.Sprintf("%s | %s (@%s, ID %s) | message %s\n%s", m.Timestamp.UTC().Format("2006-01-02 15:04:05 UTC"), display, m.Author.Username, m.Author.ID, m.ID, m.Content)
		if m.Referenced != nil {
			line += fmt.Sprintf("\nReply: @%s (message %s): %s", m.Referenced.Author.Username, m.Referenced.ID, strings.Join(strings.Fields(m.Referenced.Content), " "))
		} else if m.Reference.Message != "" {
			line += "\nReply to " + m.Reference.Message
		}
		for _, part := range m.Parts() {
			line += "\n" + part
		}
		for i, a := range m.Files() {
			line += fmt.Sprintf("\nAttachment %d: %s (%d bytes)", i+1, a.Name, a.Size)
		}
		lines = append(lines, line)
	}
	if fileStatus != "" {
		lines = append(lines, fileStatus)
	}
	return contract.DiscordReadResponse{Channel: channel, Text: contract.Outside("discord channel "+channel, "", strings.Join(lines, "\n\n"))}, nil
}
