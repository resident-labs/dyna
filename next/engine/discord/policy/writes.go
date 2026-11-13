package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"

	"time"
)

// Facts contains live Discord metadata, never assertions from a write body.
type Facts struct {
	Now             time.Time
	OwnMessage      bool
	CategoryInGuild func(string) bool
	RolePermissions uint64
}

const (
	Administrator        uint64 = 1 << 3
	ManageGuild          uint64 = 1 << 5
	ManageRoles          uint64 = 1 << 28
	ManageChannels       uint64 = 1 << 4
	KickMembers          uint64 = 1 << 1
	BanMembers           uint64 = 1 << 2
	ModerateMembers      uint64 = 1 << 40
	ManageWebhooks       uint64 = 1 << 29
	MentionEveryone      uint64 = 1 << 17
	DangerousPermissions        = Administrator | ManageGuild | ManageRoles | ManageChannels | KickMembers | BanMembers | ModerateMembers | ManageWebhooks | MentionEveryone
)

func bodyInto(body json.RawMessage, out any) error {
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("body must be a JSON object")
	}
	if err := exactJSON(json.NewDecoder(bytes.NewReader(body)), reflect.TypeOf(out)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

type Mentions struct {
	Parse       []string `json:"parse"`
	Users       []string `json:"users,omitempty"`
	Roles       []string `json:"roles,omitempty"`
	RepliedUser bool     `json:"replied_user,omitempty"`
}
type Message struct {
	Poll       json.RawMessage   `json:"poll,omitempty"`
	Components []json.RawMessage `json:"components,omitempty"`
	Stickers   []string          `json:"sticker_ids,omitempty"`
	Flags      uint64            `json:"flags,omitempty"`
	Embeds     []json.RawMessage `json:"embeds,omitempty"`
	Content    string            `json:"content,omitempty"`
	Mentions   *Mentions         `json:"allowed_mentions,omitempty"`
	Reference  *struct {
		Message string `json:"message_id"`
		Channel string `json:"channel_id,omitempty"`
		Guild   string `json:"guild_id,omitempty"`
		Fail    *bool  `json:"fail_if_not_exists,omitempty"`
	} `json:"message_reference,omitempty"`
}

func safeMessage(message Message) bool {
	if message.Flags & ^uint64((1<<2)|(1<<12)) != 0 {
		return false
	}
	if len(message.Poll) > 0 && bytes.TrimSpace(message.Poll)[0] != '{' {
		return false
	}
	for _, component := range message.Components {
		trimmed := bytes.TrimSpace(component)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return false
		}
	}
	if len(message.Embeds) > 10 {
		return false
	}
	for _, embed := range message.Embeds {
		trimmed := bytes.TrimSpace(embed)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return false
		}
	}
	if message.Mentions == nil {
		message.Mentions = &Mentions{Parse: []string{}}
	}
	if len(message.Mentions.Roles) > 0 {
		return false
	}
	for _, s := range message.Mentions.Parse {
		if s != "users" {
			return false
		}
	}
	return true
}

// Write checks a normalized route without retaining its normalized send body.
func (p Policy) Write(method, path string, body json.RawMessage, facts Facts) error {
	_, err := p.Authorize(ParseRoute(method, path), body, facts)
	return err
}

// Authorize checks a classified route and returns the body to forward. Resource
// scope remains the engine's responsibility. Missing mentions are suppressed.
func (p Policy) Authorize(route Route, body json.RawMessage, facts Facts) (json.RawMessage, error) {
	deny := func(cap string) (json.RawMessage, error) {
		return nil, fmt.Errorf("the server's policy limits the answering session's %s; ask a moderator or your parent", cap)
	}
	switch route.Action {
	case Read:
		return body, nil
	case WebhookRead:
		return nil, route.Refusal()
	case ChannelEdit:
		if route.Resource != Channel {
			return nil, route.Refusal()
		}
		var edit struct {
			Topic    json.RawMessage `json:"topic"`
			Slowmode json.RawMessage `json:"rate_limit_per_user"`
		}
		if bodyInto(body, &edit) != nil {
			return nil, route.Refusal()
		}
		if len(edit.Topic) > 0 && len(edit.Slowmode) == 0 && slices.Contains(p.Front.Channels, "topic") {
			var topic *string
			if json.Unmarshal(edit.Topic, &topic) == nil {
				return body, nil
			}
		}
		if len(edit.Slowmode) > 0 && len(edit.Topic) == 0 && slices.Contains(p.Front.Channels, "slowmode") {
			var seconds *int
			if json.Unmarshal(edit.Slowmode, &seconds) == nil && seconds != nil && *seconds >= 0 && *seconds <= 21600 {
				return body, nil
			}
		}
		return nil, route.Refusal()
	case ChannelCreate:
		var channel struct {
			Name   string          `json:"name"`
			Topic  *string         `json:"topic"`
			Parent json.RawMessage `json:"parent_id"`
			Type   json.RawMessage `json:"type"`
		}
		if !slices.Contains(p.Front.Channels, "create") || bodyInto(body, &channel) != nil || channel.Name == "" {
			return nil, route.Refusal()
		}
		if len(channel.Type) > 0 {
			var kind *int
			if json.Unmarshal(channel.Type, &kind) != nil || kind == nil || *kind != 0 {
				return nil, route.Refusal()
			}
		}
		if len(channel.Parent) > 0 {
			var parent *string
			if json.Unmarshal(channel.Parent, &parent) != nil || parent == nil || facts.CategoryInGuild == nil || !facts.CategoryInGuild(*parent) {
				return nil, route.Refusal()
			}
		}
		return body, nil
	case MemberTimeout:
		var timeout struct {
			Until json.RawMessage `json:"communication_disabled_until"`
		}
		if bodyInto(body, &timeout) != nil || len(timeout.Until) == 0 {
			return deny("timeout fields")
		}
		if string(timeout.Until) == "null" {
			return body, nil
		}
		var timestamp string
		if json.Unmarshal(timeout.Until, &timestamp) != nil {
			return deny("timeout_max")
		}
		until, err := time.Parse(time.RFC3339Nano, timestamp)
		maximum, _ := time.ParseDuration(p.Front.TimeoutMax)
		if err != nil || maximum == 0 || until.After(facts.Now.Add(maximum)) {
			return nil, fmt.Errorf("the server's policy lets the answering session time a member out for at most %s; ask a moderator or your parent", p.Front.TimeoutMax)
		}
		return body, nil
	case MemberRole:
		var empty struct{}
		if bodyInto(body, &empty) != nil {
			return deny("role fields")
		}
		for _, roleID := range p.Front.Roles {
			if roleID == route.RoleID && facts.RolePermissions&DangerousPermissions == 0 {
				return body, nil
			}
		}
		return deny("roles and safe role permissions")
	case MessageCreate, MessageEdit:
		var message Message
		if bodyInto(body, &message) != nil {
			return deny("message fields")
		}
		if route.Action == MessageEdit && (message.Reference != nil || !facts.OwnMessage) {
			return deny("editing its own message")
		}
		if !safeMessage(message) {
			return deny("message fields and allowed_mentions")
		}
		return suppressMissingMentions(body, false)
	case PollExpire:
		var empty struct{}
		if !facts.OwnMessage || bodyInto(body, &empty) != nil {
			return deny("ending its own poll")
		}
		return body, nil
	case MessageDelete:
		messageID, err := strconv.ParseUint(route.MessageID, 10, 64)
		created := time.UnixMilli(int64(messageID>>22) + 1420070400000)
		var empty struct{}
		if !bool(p.Front.Delete) || err != nil || created.Before(facts.Now.Add(-14*24*time.Hour)) || bodyInto(body, &empty) != nil {
			return deny("delete of one message under 14 days")
		}
		return body, nil
	case Pin, Reaction:
		var empty struct{}
		if bodyInto(body, &empty) == nil {
			return body, nil
		}
		return deny("pin and reaction fields")
	case ThreadCreate:
		if !p.Front.Threads {
			return deny("threads")
		}
		var thread struct {
			Name      string   `json:"name"`
			Archive   int      `json:"auto_archive_duration,omitempty"`
			Type      int      `json:"type,omitempty"`
			Invitable *bool    `json:"invitable,omitempty"`
			Message   *Message `json:"message,omitempty"`
			Tags      []string `json:"applied_tags,omitempty"`
		}
		if bodyInto(body, &thread) != nil || thread.Name == "" || (!route.Forum && (thread.Message != nil || len(thread.Tags) > 0)) {
			return deny("thread fields")
		}
		if thread.Message != nil {
			if !safeMessage(*thread.Message) {
				return deny("thread message fields")
			}
			if thread.Message.Mentions == nil {
				thread.Message.Mentions = &Mentions{Parse: []string{}}
			}
		}
		return suppressMissingMentions(body, true)
	default:
		return nil, route.Refusal()
	}
}

// Preserve explicitly supplied empty content, rich-message arrays and zero flags
// when suppressing mentions, so edits can clear those fields too.
func suppressMissingMentions(body json.RawMessage, thread bool) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	if thread {
		if message, ok := fields["message"]; ok && string(bytes.TrimSpace(message)) != "null" {
			normalized, err := suppressMissingMentions(message, false)
			if err != nil {
				return nil, err
			}
			fields["message"] = normalized
		}
	} else if mentions, ok := fields["allowed_mentions"]; !ok || string(mentions) == "null" {
		fields["allowed_mentions"] = json.RawMessage(`{"parse":[]}`)
	}
	return json.Marshal(fields)
}
