package engine

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/policy"
	"github.com/resident-labs/dyna/engine/store"
)

type discordAnswerWindow struct {
	Start   time.Time
	Count   int
	Reacted bool
}

// Answer budgets deliberately reset at engine restart. The map is bounded; at
// capacity new keys fail closed until an old window expires.
func (s *Service) discordPolicyInput(ctx context.Context, client *discord.Client, m discord.Message, routes []store.DiscordRoute, selected store.DiscordRoute) (bool, error) {
	if m.Guild == "" {
		return true, nil
	}
	for _, r := range routes {
		if r.Guild != m.Guild || r.Room != selected.Room || r.Channel != "" || r.PolicyResolved == "" {
			continue
		}
		var p policy.Policy
		if e := json.Unmarshal([]byte(r.PolicyResolved), &p); e != nil {
			return false, e
		}
		if !p.On() {
			return true, nil
		}
		channel, _ := s.discordDirectory.Channel(m.Channel)
		for _, quiet := range p.Front.Quiet {
			if quiet == m.Channel {
				return false, nil
			}
			if strings.HasSuffix(quiet, "/*") {
				category := strings.TrimSuffix(quiet, "/*")
				parent := channel.Parent
				if channel.Type == 10 || channel.Type == 11 || channel.Type == 12 {
					c, _ := s.discordDirectory.Channel(parent)
					parent = c.Parent
				}
				if parent == category {
					return false, nil
				}
			}
		}
		memberMax, _ := policy.Budget(p.Front.Answers.PerMember)
		channelMax, _ := policy.Budget(p.Front.Answers.PerChannel)
		if memberMax == 0 && channelMax == 0 {
			return true, nil
		}
		now := time.Now()
		s.discordRuntime.Lock()
		if s.discordAnswers == nil {
			s.discordAnswers = map[string]discordAnswerWindow{}
		}
		for k, w := range s.discordAnswers {
			if now.Sub(w.Start) >= time.Hour {
				delete(s.discordAnswers, k)
			}
		}
		memberKey := m.Guild + "/member/" + m.Author.ID
		channelKey := m.Guild + "/channel/" + m.Channel
		member := s.discordAnswers[memberKey]
		channelWindow := s.discordAnswers[channelKey]
		if member.Start.IsZero() {
			member.Start = now
		}
		if channelWindow.Start.IsZero() {
			channelWindow.Start = now
		}
		over := len(s.discordAnswers) >= 10000 || (memberMax > 0 && member.Count >= memberMax) || (channelMax > 0 && channelWindow.Count >= channelMax)
		react := over && !member.Reacted && len(s.discordAnswers) < 10000
		if !over {
			member.Count++
			channelWindow.Count++
		}
		if react {
			member.Reacted = true
		}
		if len(s.discordAnswers) < 10000 {
			s.discordAnswers[memberKey] = member
			s.discordAnswers[channelKey] = channelWindow
		}
		s.discordRuntime.Unlock()
		if react {
			_, _ = client.API(ctx, "PUT", "/api/v10/channels/"+m.Channel+"/messages/"+m.ID+"/reactions/%E2%8F%B3/@me", nil, "")
		}
		return !over, nil
	}
	return true, nil
}
