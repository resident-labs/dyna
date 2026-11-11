package contract

import "encoding/json"

const MethodRoomDiscord Method = "room.discord"
const MethodDiscordAPI Method = "discord.api"
const MethodDiscordSend Method = "discord.send"

const MethodDiscordConfigure Method = "discord.configure"

// DiscordConfigureRequest carries credentials only on the owner socket.
type DiscordConfigureRequest struct {
	Token string  `json:"token,omitempty"`
	Owner *string `json:"owner,omitempty"`
	Clear bool    `json:"clear,omitempty"`
}
type DiscordRoute struct {
	Instructions string    `json:"instructions,omitempty"`
	Guild        string    `json:"guild"`
	Channel      string    `json:"channel,omitempty"`
	Room         RoomID    `json:"room"`
	Session      SessionID `json:"session"`
}
type DiscordConfigureResponse struct {
	BotID   string         `json:"botID"`
	BotName string         `json:"botName"`
	Owner   string         `json:"owner"`
	Routes  []DiscordRoute `json:"routes,omitempty"`
}
type RoomDiscordRequest struct {
	Instructions *string `json:"instructions,omitempty"`
	Room         RoomID  `json:"room"`
	Server       string  `json:"server,omitempty"`
	Channel      string  `json:"channel,omitempty"`
	Session      string  `json:"session,omitempty"`
	Remove       bool    `json:"remove,omitempty"`
}
type RoomDiscordResponse struct {
	Routes []DiscordRoute `json:"routes,omitempty"`
}
type DiscordAPIRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
	Reason string          `json:"reason,omitempty"`
}
type DiscordAPIResponse struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}
type DiscordSendRequest struct {
	Channel     string       `json:"channel"`
	Text        string       `json:"text"`
	ReplyTo     string       `json:"replyTo,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Paths       []string     `json:"paths,omitempty"`
}
type DiscordSendResponse struct {
	Messages []string `json:"messages"`
}

type DiscordAttachment struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

// DiscordMessage is text addressed to the room's bot by a human; only Owner is trusted.
type DiscordMessage struct {
	Channel     string              `json:"channel"`
	ChannelName string              `json:"channelName"`
	Thread      string              `json:"thread,omitempty"`
	GuildName   string              `json:"guildName"`
	Author      string              `json:"author"`
	AuthorID    string              `json:"authorID"`
	DisplayName string              `json:"displayName"`
	Roles       []string            `json:"roles,omitempty"`
	Owner       bool                `json:"owner,omitempty"`
	Message     string              `json:"message"`
	Text        string              `json:"text"`
	History     string              `json:"history,omitempty"`
	Attachments []DiscordAttachment `json:"attachments,omitempty"`
}

func (DiscordMessage) EventType() EventType { return "discord.message" }
func (DiscordMessage) isEventBody()         {}
