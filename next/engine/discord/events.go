package discord

import (
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ServerEvent is a change on a server that its room hears of, found by the gateway beside the directory update.
type ServerEvent struct {
	Guild string
	Body  contract.EventBody
}

// discordEpoch is the start of Discord's snowflake clock, in Unix milliseconds.
const discordEpoch = 1420070400000

// serverEvent finds the room event a dispatch stands for. It runs before the directory applies the dispatch, because what
// a channel or role update changed is read from the directory's previous value. A create of what the directory already
// holds, and a delete or an update of what it does not, are replays or unknowns and give no event.
func (d *Directory) serverEvent(kind string, data json.RawMessage) (ServerEvent, bool) {
	switch kind {
	case "GUILD_MEMBER_ADD", "GUILD_MEMBER_REMOVE":
		var member struct {
			Guild string `json:"guild_id"`
			Nick  string `json:"nick"`
			User  struct {
				ID         string `json:"id"`
				Username   string `json:"username"`
				GlobalName string `json:"global_name"`
				Bot        bool   `json:"bot"`
			} `json:"user"`
		}
		if json.Unmarshal(data, &member) != nil || member.Guild == "" {
			return ServerEvent{}, false
		}
		id, err := strconv.ParseUint(member.User.ID, 10, 64)
		if err != nil {
			return ServerEvent{}, false
		}
		name := member.Nick
		for _, fallback := range []string{member.User.GlobalName, member.User.Username} {
			if name == "" {
				name = fallback
			}
		}
		joined := contract.DiscordMemberJoined{Server: member.Guild, User: member.User.ID, Name: name, Created: time.UnixMilli(int64(id>>22) + discordEpoch).UTC(), Bot: member.User.Bot}
		if kind == "GUILD_MEMBER_REMOVE" {
			return ServerEvent{member.Guild, contract.DiscordMemberLeft(joined)}, true
		}
		return ServerEvent{member.Guild, joined}, true
	case "CHANNEL_CREATE", "CHANNEL_UPDATE", "CHANNEL_DELETE":
		var channel Channel
		if json.Unmarshal(data, &channel) != nil || channel.ID == "" || channel.Guild == "" {
			return ServerEvent{}, false
		}
		before, known := d.Channel(channel.ID)
		parent, _ := d.Channel(channel.Parent)
		created := contract.DiscordChannelCreated{Server: channel.Guild, Channel: channel.ID, Name: channel.Name, Kind: channel.Type, Parent: channel.Parent, ParentName: parent.Name}
		switch {
		case kind == "CHANNEL_CREATE" && !known:
			return ServerEvent{channel.Guild, created}, true
		case kind == "CHANNEL_DELETE" && known:
			return ServerEvent{channel.Guild, contract.DiscordChannelDeleted(created)}, true
		case kind == "CHANNEL_UPDATE" && known:
			var changed []string
			if channel.Name != before.Name {
				changed = append(changed, "name")
			}
			if channel.Parent != before.Parent {
				changed = append(changed, "parent")
			}
			if movedFar(channel.Position, before.Position) {
				changed = append(changed, "position")
			}
			if channel.Topic != before.Topic {
				changed = append(changed, "topic")
			}
			if !slices.Equal(channel.Overwrites, before.Overwrites) {
				changed = append(changed, "overwrites")
			}
			if len(changed) > 0 {
				return ServerEvent{channel.Guild, contract.DiscordChannelUpdated{Server: created.Server, Channel: created.Channel, Name: created.Name, Kind: created.Kind, Parent: created.Parent, ParentName: created.ParentName, Changed: changed}}, true
			}
		}
	case "GUILD_ROLE_CREATE", "GUILD_ROLE_UPDATE", "GUILD_ROLE_DELETE":
		var event struct {
			Guild string `json:"guild_id"`
			Role  Role   `json:"role"`
			ID    string `json:"role_id"`
		}
		if json.Unmarshal(data, &event) != nil || event.Guild == "" {
			return ServerEvent{}, false
		}
		if kind == "GUILD_ROLE_DELETE" {
			event.Role.ID = event.ID
		}
		if event.Role.ID == "" {
			return ServerEvent{}, false
		}
		server, _ := d.Server(event.Guild)
		index := slices.IndexFunc(server.Roles, func(role Role) bool { return role.ID == event.Role.ID })
		created := contract.DiscordRoleCreated{Server: event.Guild, Role: event.Role.ID, Name: event.Role.Name}
		switch {
		case kind == "GUILD_ROLE_CREATE" && index < 0:
			return ServerEvent{event.Guild, created}, true
		case kind == "GUILD_ROLE_DELETE" && index >= 0:
			created.Name = server.Roles[index].Name
			return ServerEvent{event.Guild, contract.DiscordRoleDeleted(created)}, true
		case kind == "GUILD_ROLE_UPDATE" && index >= 0:
			before := server.Roles[index]
			var changed []string
			if event.Role.Name != before.Name {
				changed = append(changed, "name")
			}
			if movedFar(event.Role.Position, before.Position) {
				changed = append(changed, "position")
			}
			if event.Role.Permissions != before.Permissions {
				changed = append(changed, "permissions")
			}
			if len(changed) > 0 {
				return ServerEvent{event.Guild, contract.DiscordRoleUpdated{Server: created.Server, Role: created.Role, Name: created.Name, Changed: changed}}, true
			}
		}
	}
	return ServerEvent{}, false
}

// movedFar says whether a position changed by more than one place. Dragging one channel or role sends an update for each
// neighbour it shifted, and each shifts by exactly one, so those are folded away; the dragged one moved by one only
// when it swapped with a neighbour, and that goes unreported too.
func movedFar(now, before int) bool {
	return now-before > 1 || before-now > 1
}
