package contract

import (
	"encoding/json"
	"time"
)

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
	Policy       string    `json:"policy,omitempty"`
	Instructions string    `json:"instructions,omitempty"`
	Guild        string    `json:"guild"`
	Channel      string    `json:"channel,omitempty"`
	Room         RoomID    `json:"room"`
	Session      SessionID `json:"session"`
}
type DiscordConfigureResponse struct {
	GatewayError string         `json:"gatewayError,omitempty"`
	BotID        string         `json:"botID"`
	BotName      string         `json:"botName"`
	Owner        string         `json:"owner"`
	Routes       []DiscordRoute `json:"routes,omitempty"`
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
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	URL    string `json:"url"`
	Status string `json:"status,omitempty"`
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
	RichContent string              `json:"richContent,omitempty"`
	History     string              `json:"history,omitempty"`
	Attachments []DiscordAttachment `json:"attachments,omitempty"`
	Files       []Attachment        `json:"files,omitempty"`
}

func (DiscordMessage) EventType() EventType { return "discord.message" }
func (DiscordMessage) isEventBody()         {}

const MethodDiscordInstructions Method = "discord.instructions"

// DiscordInstructionsRequest reads, or with Text sets, the standing instructions of the mappings Session answers.
type DiscordInstructionsRequest struct {
	Session string  `json:"session"`
	Text    *string `json:"text,omitempty"`
}

type DiscordInstructionsResponse struct {
	Text string `json:"text"`
}

const MethodDiscordRead Method = "discord.read"

type DiscordReadRequest struct {
	Channel string `json:"channel"`
	Message string `json:"message,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
	Around  string `json:"around,omitempty"`
}
type DiscordReadResponse struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// DiscordFilesRetrieved carries files requested by a running session, not a new Discord input.
type DiscordFilesRetrieved struct {
	Channel     string              `json:"channel"`
	ChannelName string              `json:"channelName"`
	Message     string              `json:"message"`
	Author      string              `json:"author"`
	AuthorID    string              `json:"authorID"`
	Files       []Attachment        `json:"files,omitempty"`
	Attachments []DiscordAttachment `json:"attachments,omitempty"`
}

func (DiscordFilesRetrieved) EventType() EventType { return "discord.files_retrieved" }
func (DiscordFilesRetrieved) isEventBody()         {}

const MethodDiscordExport Method = "discord.export"
const MethodDiscordPlan Method = "discord.plan"
const MethodDiscordApply Method = "discord.apply"

type DiscordExportRequest struct {
	Room   RoomID `json:"room,omitempty"`
	Server string `json:"server,omitempty"`
	Force  bool   `json:"force,omitempty"`
}
type DiscordPlanRequest struct {
	Room   RoomID `json:"room,omitempty"`
	Server string `json:"server,omitempty"`
	Prune  bool   `json:"prune,omitempty"`
}
type DiscordApplyRequest struct {
	Room   RoomID `json:"room,omitempty"`
	Server string `json:"server,omitempty"`
	Prune  bool   `json:"prune,omitempty"`
}
type DiscordFolderResponse struct {
	Text string `json:"text"`
}

const MethodDiscordPolicy Method = "discord.policy"

type DiscordPolicyRequest struct {
	Room   RoomID  `json:"room,omitempty"`
	Server string  `json:"server,omitempty"`
	Text   *string `json:"text,omitempty"`
}
type DiscordPolicyResponse struct {
	Text string `json:"text"`
}

const MethodDiscordCheck Method = "discord.check"

type DiscordCheckRequest struct {
	Room   RoomID `json:"room,omitempty"`
	Server string `json:"server,omitempty"`
}
type DiscordCheckResponse struct {
	Lines []string `json:"lines"`
}

// Server events are what happens on a Discord server whose whole-server mapping a room holds. Names are chosen by
// members or admins, so the renderer marks them as outside text. The invite used is not known.

// DiscordMemberJoined says a member joined the server.
type DiscordMemberJoined struct {
	Server  string    `json:"server"`
	User    string    `json:"user"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"` // when the account was made, from its ID
	Bot     bool      `json:"bot,omitempty"`
}

// DiscordMemberLeft says a member is no longer in the server; Discord does not say whether they left or were removed.
type DiscordMemberLeft DiscordMemberJoined

// DiscordChannelCreated says a channel or category was created.
type DiscordChannelCreated struct {
	Server     string `json:"server"`
	Channel    string `json:"channel"`
	Name       string `json:"name"`
	Kind       int    `json:"kind"` // Discord's channel type number
	Parent     string `json:"parent,omitempty"`
	ParentName string `json:"parentName,omitempty"`
}

// DiscordChannelDeleted says a channel or category was deleted.
type DiscordChannelDeleted DiscordChannelCreated

// DiscordChannelUpdated says a channel or category changed; Changed lists name, parent, position, topic or overwrites.
// A position that moved by one place is not listed, because dragging one channel shifts its neighbours by one.
type DiscordChannelUpdated struct {
	Server     string   `json:"server"`
	Channel    string   `json:"channel"`
	Name       string   `json:"name"`
	Kind       int      `json:"kind"`
	Parent     string   `json:"parent,omitempty"`
	ParentName string   `json:"parentName,omitempty"`
	Changed    []string `json:"changed"`
}

// DiscordRoleCreated says a role was created.
type DiscordRoleCreated struct {
	Server string `json:"server"`
	Role   string `json:"role"`
	Name   string `json:"name"`
}

// DiscordRoleDeleted says a role was deleted.
type DiscordRoleDeleted DiscordRoleCreated

// DiscordRoleUpdated says a role changed; Changed lists name, position or permissions.
// A position that moved by one place is not listed, as for channels.
type DiscordRoleUpdated struct {
	Server  string   `json:"server"`
	Role    string   `json:"role"`
	Name    string   `json:"name"`
	Changed []string `json:"changed"`
}

func (DiscordMemberJoined) EventType() EventType   { return "discord.member.joined" }
func (DiscordMemberJoined) isEventBody()           {}
func (DiscordMemberLeft) EventType() EventType     { return "discord.member.left" }
func (DiscordMemberLeft) isEventBody()             {}
func (DiscordChannelCreated) EventType() EventType { return "discord.channel.created" }
func (DiscordChannelCreated) isEventBody()         {}
func (DiscordChannelUpdated) EventType() EventType { return "discord.channel.updated" }
func (DiscordChannelUpdated) isEventBody()         {}
func (DiscordChannelDeleted) EventType() EventType { return "discord.channel.deleted" }
func (DiscordChannelDeleted) isEventBody()         {}
func (DiscordRoleCreated) EventType() EventType    { return "discord.role.created" }
func (DiscordRoleCreated) isEventBody()            {}
func (DiscordRoleUpdated) EventType() EventType    { return "discord.role.updated" }
func (DiscordRoleUpdated) isEventBody()            {}
func (DiscordRoleDeleted) EventType() EventType    { return "discord.role.deleted" }
func (DiscordRoleDeleted) isEventBody()            {}
