package folder

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/resident-labs/dyna/engine/discord"
)

func snapshotBotPosition(ctx context.Context, client *discord.Client, server string, roles []Role) (int, error) {
	if client.BotID == "" {
		return 0, nil
	}
	result, err := client.ReadConfiguration(ctx, server, "/members/"+client.BotID)
	if err != nil {
		return 0, err
	}
	if result.Status != 200 {
		return 0, fmt.Errorf("cannot read the bot's server roles: HTTP %d", result.Status)
	}
	var member struct {
		Roles []string `json:"roles"`
	}
	if err := json.Unmarshal(result.Body, &member); err != nil {
		return 0, fmt.Errorf("cannot decode the bot's server roles: %w", err)
	}
	highest := 0
	for _, role := range roles {
		if role.Position > highest && slices.Contains(member.Roles, role.ID) {
			highest = role.Position
		}
	}
	return highest, nil
}
