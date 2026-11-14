package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/resident-labs/dyna/engine/discord"
)

// Guild metadata follows READY. Catch up once it is available, so threads and
// role mentions use the same directory as live intake. RESUMED does not do this.
func (s *Service) discordCatchUp(ctx context.Context, client *discord.Client, guild string, receive func(context.Context, discord.Message) error) {
	routes, err := s.store.DiscordRoutes(ctx, "")
	if err != nil {
		log.Printf("Discord catch-up: %v", err)
		return
	}
	channels := map[string]bool{}
	server, _ := s.discordDirectory.Server(guild)
	for _, r := range routes {
		if r.Guild != guild {
			continue
		}
		if r.Channel != "" {
			channels[r.Channel] = true
		}
		for _, c := range append(server.Channels, server.Threads...) {
			if (r.Channel == "" || c.Parent == r.Channel) && (c.Type == 0 || c.Type == 5 || c.Type == 10 || c.Type == 11 || c.Type == 12) {
				channels[c.ID] = true
			}
		}
	}
	for channel := range channels {
		count, err := s.discordCatchUpChannel(ctx, client, guild, channel, receive)
		log.Printf("Discord catch-up channel %s: %d messages, error: %v", channel, count, err)
	}
}

func (s *Service) discordCatchUpChannel(ctx context.Context, client *discord.Client, guild, channel string, receive func(context.Context, discord.Message) error) (int, error) {
	floor := uint64(time.Now().Add(-10*time.Minute).UnixMilli()-1420070400000) << 22
	after := strconv.FormatUint(floor, 10)
	count := 0
	for {
		result, err := client.ReadMessages(ctx, "/api/v10/channels/"+channel+"/messages?after="+after+"&limit=100")
		if err != nil {
			return count, err
		}
		if result.Status != 200 {
			return count, fmt.Errorf("messages returned %d", result.Status)
		}
		var messages []discord.Message
		if err := json.Unmarshal(result.Body, &messages); err != nil {
			return count, err
		}
		sort.Slice(messages, func(i, j int) bool {
			a, _ := strconv.ParseUint(messages[i].ID, 10, 64)
			b, _ := strconv.ParseUint(messages[j].ID, 10, 64)
			return a < b
		})
		// Deliver in chronological order.
		for i := 0; i < len(messages); i++ {
			m := messages[i]
			if m.Author.Bot || m.Webhook != "" || m.Type == 24 {
				after = m.ID
				continue
			}
			id, err := strconv.ParseUint(m.ID, 10, 64)
			if err != nil {
				return count, err
			}
			if id < floor {
				continue
			}
			m.Guild = guild
			m.Channel = channel
			// REST messages omit the gateway member. Resolve it at intake, rather than
			// weakening role policy for recovered messages.
			member, err := client.API(ctx, "GET", "/api/v10/guilds/"+guild+"/members/"+m.Author.ID, nil, "")
			if err != nil {
				return count, err
			}
			if member.Status == 404 {
				after = m.ID
				continue
			}
			if member.Status != 200 {
				return count, fmt.Errorf("member returned %d", member.Status)
			}
			if err := json.Unmarshal(member.Body, &m.Member); err != nil {
				return count, err
			}
			if err := receive(ctx, m); err != nil {
				return count, err
			}
			after = m.ID
			count++
		}
		if len(messages) < 100 {
			return count, nil
		}
	}
}
