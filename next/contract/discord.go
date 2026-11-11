package contract

import "encoding/json"

const MethodRoomDiscord Method = "room.discord"
const MethodDiscordAPI Method = "discord.api"
const MethodDiscordSend Method = "discord.send"

// RoomDiscordRequest carries credentials only on the owner socket.
type RoomDiscordRequest struct {
	Room    RoomID `json:"room"`
	Session string `json:"session,omitempty"`
	Token   string `json:"token,omitempty"`
	Clear   bool   `json:"clear,omitempty"`
}
type RoomDiscordResponse struct {
	Guild     string    `json:"guild"`
	GuildName string    `json:"guildName"`
	BotID     string    `json:"botID"`
	BotName   string    `json:"botName"`
	Session   SessionID `json:"session"`
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

// DiscordMessage is untrusted text addressed to the room's bot by a human.
type DiscordMessage struct {
	Channel     string   `json:"channel"`
	ChannelName string   `json:"channelName"`
	Thread      string   `json:"thread,omitempty"`
	GuildName   string   `json:"guildName"`
	Author      string   `json:"author"`
	AuthorID    string   `json:"authorID"`
	Message     string   `json:"message"`
	Text        string   `json:"text"`
	History     string   `json:"history,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

func (DiscordMessage) EventType() EventType { return "discord.message" }
func (DiscordMessage) isEventBody()         {}
