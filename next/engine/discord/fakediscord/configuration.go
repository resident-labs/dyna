package fakediscord

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Request records every REST or gateway HTTP request, including every write body.
type Request struct{ Method, Path, Body string }

func (s *Server) RequestLog() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requestLog...)
}
func (s *Server) Writes() []Request {
	var writes []Request
	for _, r := range s.RequestLog() {
		if r.Method != "GET" && r.Method != "HEAD" {
			writes = append(writes, r)
		}
	}
	return writes
}

// Configuration supplies typed REST fixture responses; it is independent of
// gateway events. Install before issuing requests.
type Configuration struct {
	Guild    Guild
	Roles    []Role
	Channels []Channel
	AutoMod  []AutoMod
	BotRoles []string // IDs of the roles held by the bot user (900)
}
type Guild struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Verification  int      `json:"verification_level"`
	Notifications int      `json:"default_message_notifications"`
	Features      []string `json:"features"`
	Rules         string   `json:"rules_channel_id"`
	Updates       string   `json:"public_updates_channel_id"`
}
type Role struct {
	Icon         string `json:"icon,omitempty"`
	UnicodeEmoji string `json:"unicode_emoji,omitempty"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Permissions  string `json:"permissions"`
	Position     int    `json:"position"`
	Managed      bool   `json:"managed"`
	Color        int    `json:"color"`
	Hoist        bool   `json:"hoist"`
	Mentionable  bool   `json:"mentionable"`
}
type Overwrite struct {
	ID    string `json:"id"`
	Type  int    `json:"type"`
	Allow string `json:"allow"`
	Deny  string `json:"deny"`
}
type Tag struct {
	EmojiName string `json:"emoji_name,omitempty"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Moderated bool   `json:"moderated"`
}
type Channel struct {
	NSFW          bool        `json:"nsfw,omitempty"`
	Flags         int         `json:"flags,omitempty"`
	DefaultLayout int         `json:"default_forum_layout,omitempty"`
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Type          int         `json:"type"`
	Position      int         `json:"position"`
	Parent        string      `json:"parent_id,omitempty"`
	Topic         string      `json:"topic"`
	Slowmode      int         `json:"rate_limit_per_user"`
	Overwrites    []Overwrite `json:"permission_overwrites"`
	Tags          []Tag       `json:"available_tags,omitempty"`
}
type AutoMod struct {
	ExemptRoles    []string        `json:"exempt_roles,omitempty"`
	ExemptChannels []string        `json:"exempt_channels,omitempty"`
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Event          int             `json:"event_type"`
	Trigger        int             `json:"trigger_type"`
	Enabled        bool            `json:"enabled"`
	Metadata       TriggerMetadata `json:"trigger_metadata"`
	Actions        []Action        `json:"actions"`
}
type TriggerMetadata struct {
	Regex    []string `json:"regex_patterns,omitempty"`
	Allowed  []string `json:"allow_list,omitempty"`
	Keywords []string `json:"keyword_filter"`
}
type Action struct {
	Type     int            `json:"type"`
	Metadata ActionMetadata `json:"metadata"`
}
type ActionMetadata struct {
	Custom  string `json:"custom_message,omitempty"`
	Channel string `json:"channel_id,omitempty"`
}

func (s *Server) SetConfiguration(c Configuration) {
	// Copy through JSON so mutations of a caller's slices cannot race with HTTP.
	b, _ := json.Marshal(c)
	var copy Configuration
	_ = json.Unmarshal(b, &copy)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configuration = &copy
}
func (s *Server) configurationGET(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.configuration
	if c == nil || r.Method != "GET" {
		return false
	}
	if channel := c.channel(strings.TrimPrefix(r.URL.Path, "/api/v10/channels/")); channel != nil {
		_ = json.NewEncoder(w).Encode(channel)
		return true
	}
	base := "/api/v10/guilds/" + c.Guild.ID
	if r.URL.Path != base && !strings.HasPrefix(r.URL.Path, base+"/") {
		return false
	}
	var v any
	switch strings.TrimPrefix(r.URL.Path, base) {
	case "":
		v = c.Guild
	case "/roles":
		v = c.Roles
	case "/channels":
		v = c.Channels
	case "/auto-moderation/rules":
		v = c.AutoMod
	case "/members/900":
		v = map[string][]string{"roles": c.BotRoles}
	default:
		return false
	}
	_ = json.NewEncoder(w).Encode(v)
	return true
}

// CommunityConfiguration includes synchronized and independent overwrites,
// forum tags, announcements, member permissions, a bot role, and an externally
// owned category. Ownership itself is marked locally with UNMANAGED.md.
func CommunityConfiguration() Configuration {
	shared := []Overwrite{{ID: "100", Allow: "0", Deny: "1024", Type: 0}, {ID: "10", Allow: "1024", Deny: "0", Type: 0}}
	return Configuration{
		Guild: Guild{ID: "100", Name: "Horizon", Verification: 2, Notifications: 1, Features: []string{"COMMUNITY"}, Rules: "201", Updates: "202"},
		Roles: []Role{{ID: "100", Name: "@everyone", Permissions: "1024"}, {ID: "10", Name: "Moderator", Permissions: "8192", Position: 2}, {ID: "11", Name: "Kira", Permissions: "1024", Managed: true, Position: 1}},
		Channels: []Channel{
			{ID: "200", Name: "Community", Type: 4, Position: 0, Overwrites: shared},
			{ID: "201", Name: "rules", Parent: "200", Type: 0, Position: 0, Overwrites: shared},
			{ID: "202", Name: "announcements", Parent: "200", Type: 5, Position: 1, Overwrites: shared},
			{ID: "203", Name: "help", Parent: "200", Type: 15, Position: 2, Overwrites: shared, Tags: []Tag{{ID: "301", Name: "question"}, {ID: "302", Name: "bug"}}, Slowmode: 10},
			{ID: "210", Name: "Moderation", Type: 4, Position: 1},
			{ID: "211", Name: "mod-log", Parent: "210", Type: 0, Position: 0, Overwrites: []Overwrite{{ID: "12345", Type: 1, Allow: "1024", Deny: "0"}}},
			{ID: "220", Name: "external", Type: 4, Position: 2},
			{ID: "221", Name: "mirror", Parent: "220", Type: 0, Position: 0},
		},
		BotRoles: []string{"11"},
		AutoMod:  []AutoMod{{ID: "400", Name: "invites", Event: 1, Trigger: 1, Enabled: true, Metadata: TriggerMetadata{Keywords: []string{"discord.gg/*"}}, Actions: []Action{{Type: 1}, {Type: 2, Metadata: ActionMetadata{Channel: "211"}}}}},
	}
}
