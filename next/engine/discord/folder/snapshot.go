package folder

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/engine/discord"
)

// Snapshot contains configuration only: never messages or role memberships.
// Entries retain sidebar order; IDs in every entry adopt the live objects.
type Snapshot struct {
	Server  Server
	Entries []Entry
}

type liveRole struct {
	ID, Name, Permissions       string
	Color                       int
	Hoist, Mentionable, Managed bool
	Position                    int
	Icon                        *string
	UnicodeEmoji                *string `json:"unicode_emoji"`
}
type liveOverwrite struct {
	ID          string
	Type        int
	Allow, Deny string
}
type liveChannel struct {
	ID, Name, Topic string
	Type, Position  int
	ParentID        string          `json:"parent_id"`
	Overwrites      []liveOverwrite `json:"permission_overwrites"`
	Slowmode        int             `json:"rate_limit_per_user"`
	NSFW            bool
	Tags            []struct {
		ID, Name  string
		Moderated bool
		EmojiID   *string `json:"emoji_id"`
		EmojiName *string `json:"emoji_name"`
	} `json:"available_tags"`
	Bitrate, UserLimit    int
	Flags                 int
	DefaultArchive        int             `json:"default_auto_archive_duration"`
	DefaultReaction       json.RawMessage `json:"default_reaction_emoji"`
	DefaultSort           *int            `json:"default_sort_order"`
	DefaultLayout         int             `json:"default_forum_layout"`
	DefaultThreadSlowmode int             `json:"default_thread_rate_limit_per_user"`
}

// ReadSnapshot fetches only guild configuration endpoints, bounded to 8 MiB each.
func ReadSnapshot(ctx context.Context, client *discord.Client, server string) (Snapshot, error) {
	var out Snapshot
	if server == "" || strings.Trim(server, "0123456789") != "" {
		return out, fmt.Errorf("invalid guild ID")
	}
	get := func(suffix string, dst any) error {
		r, err := client.ReadConfiguration(ctx, server, suffix)
		if err != nil {
			return err
		}
		if r.Status != 200 {
			return fmt.Errorf("snapshot %s: HTTP %d", suffix, r.Status)
		}
		if err := json.Unmarshal(r.Body, dst); err != nil {
			return fmt.Errorf("snapshot %s (possibly truncated): %w", suffix, err)
		}
		return nil
	}
	var guild struct {
		Verification  int `json:"verification_level"`
		Notifications int `json:"default_message_notifications"`
		Features      []string
		Rules         string `json:"rules_channel_id"`
		Updates       string `json:"public_updates_channel_id"`
	}
	var roles []liveRole
	var channels []liveChannel
	var rules []json.RawMessage
	if err := get("", &guild); err != nil {
		return out, err
	}
	if err := get("/roles", &roles); err != nil {
		return out, err
	}
	if err := get("/channels", &channels); err != nil {
		return out, err
	}
	if err := get("/auto-moderation/rules", &rules); err != nil {
		return out, err
	}
	if guild.Verification < 0 || guild.Verification > 4 || guild.Notifications < 0 || guild.Notifications > 1 {
		return out, fmt.Errorf("unsupported guild settings")
	}
	out.Server.Settings.Verification = []string{"none", "low", "medium", "high", "very_high"}[guild.Verification]
	out.Server.Settings.Notifications = []string{"all", "mentions"}[guild.Notifications]
	roleNames := map[string]string{server: "everyone"}
	names := map[string]bool{"everyone": true}
	sort.SliceStable(roles, func(i, j int) bool { return roles[i].Position > roles[j].Position })
	for _, r := range roles {
		if r.ID == server {
			roleNames[r.ID] = "everyone"
		} else {
			if names[r.Name] {
				return out, fmt.Errorf("ambiguous role %q", r.Name)
			}
			names[r.Name] = true
			roleNames[r.ID] = r.Name
		}
		if r.Icon != nil || r.UnicodeEmoji != nil {
			return out, fmt.Errorf("role icons cannot be represented")
		}
		p, err := permissions(r.Permissions)
		if err != nil {
			return out, err
		}
		name := r.Name
		// The role keeps Discord's @everyone name; overwrite references use everyone.
		out.Server.Roles = append(out.Server.Roles, Role{Identity: Identity{ID: r.ID}, Name: name, Permissions: p, Color: fmt.Sprintf("#%06x", r.Color), Hoist: r.Hoist, Mentionable: r.Mentionable})
	}
	sort.SliceStable(channels, func(i, j int) bool { return channels[i].Position < channels[j].Position })
	byID := map[string]string{}
	channelNames := map[string]bool{}
	for _, c := range channels {
		if !safeName(c.Name) || channelNames[c.Name] {
			return out, fmt.Errorf("unsafe or ambiguous channel/category name %q", c.Name)
		}
		channelNames[c.Name] = true
		byID[c.ID] = c.Name
	}
	for _, c := range channels {
		ow := Overwrites{}
		for _, w := range c.Overwrites {
			name := roleNames[w.ID]
			if w.Type == 1 {
				name = "member:" + w.ID
			} else if w.Type != 0 || name == "" {
				return out, fmt.Errorf("unknown overwrite target")
			}
			a, err := permissions(w.Allow)
			if err != nil {
				return out, err
			}
			d, err := permissions(w.Deny)
			if err != nil {
				return out, err
			}
			if len(a) > 0 {
				if ow.Allow == nil {
					ow.Allow = map[string][]string{}
				}
				ow.Allow[name] = a
			}
			if len(d) > 0 {
				if ow.Deny == nil {
					ow.Deny = map[string][]string{}
				}
				ow.Deny[name] = d
			}
		}
		e := Entry{Name: c.Name, Path: c.Name}
		if c.Type == 4 {
			if c.ParentID != "" {
				return out, fmt.Errorf("nested category")
			}
			e.Category = &Category{Identity: Identity{ID: c.ID}, Overwrites: ow}
			out.Server.Categories = append(out.Server.Categories, c.Name)
		} else {
			kind := map[int]string{0: "text", 2: "voice", 5: "announcement", 15: "forum"}[c.Type]
			if kind == "" {
				return out, fmt.Errorf("channel %q type %d cannot be represented", c.Name, c.Type)
			}
			if c.NSFW || c.Flags != 0 || c.DefaultThreadSlowmode != 0 || c.DefaultLayout != 0 || c.DefaultSort != nil || (len(c.DefaultReaction) > 0 && string(c.DefaultReaction) != "null") || (c.DefaultArchive != 0 && c.DefaultArchive != 1440) {
				return out, fmt.Errorf("channel %q has unrepresentable settings", c.Name)
			}
			ch := &Channel{Identity: Identity{ID: c.ID}, Overwrites: ow, Type: kind, Topic: c.Topic}
			if c.Slowmode != 0 {
				ch.Slowmode = strconv.Itoa(c.Slowmode) + "s"
			}
			tagNames := map[string]bool{}
			for _, t := range c.Tags {
				if t.Name == "" || tagNames[t.Name] {
					return out, fmt.Errorf("ambiguous forum tag %q", t.Name)
				}
				tagNames[t.Name] = true
				if t.Moderated || t.EmojiID != nil || t.EmojiName != nil {
					return out, fmt.Errorf("forum tag metadata cannot be represented")
				}
				ch.Tags = append(ch.Tags, t.Name)
			}
			e.Channel = ch
			if c.ParentID != "" {
				parent := byID[c.ParentID]
				isCategory := false
				for _, candidate := range channels {
					if candidate.ID == c.ParentID && candidate.Type == 4 {
						isCategory = true
					}
				}
				if parent == "" || !isCategory {
					return out, fmt.Errorf("missing parent")
				}
				e.Parent = parent
				e.Path = parent + "/" + c.Name
			}
		}
		out.Entries = append(out.Entries, e)
	}
	for i := range out.Entries {
		e := &out.Entries[i]
		if e.Category != nil {
			e.Category.Sync = true
			for _, child := range out.Entries {
				if child.Parent == e.Name {
					e.Category.Channels = append(e.Category.Channels, child.Name)
					if child.Channel != nil && !reflect.DeepEqual(child.Channel.Overwrites, e.Category.Overwrites) {
						e.Category.Sync = false
					}
				}
			}
		}
	}
	for _, f := range guild.Features {
		if f == "COMMUNITY" {
			if byID[guild.Rules] == "" || byID[guild.Updates] == "" {
				return out, fmt.Errorf("community channels missing")
			}
			out.Server.Settings.Community = &Community{Rules: byID[guild.Rules], Updates: byID[guild.Updates]}
		}
	}
	for _, raw := range rules {
		var r struct {
			ID, Name string
			Event    int `json:"event_type"`
			Trigger  int `json:"trigger_type"`
			Enabled  bool
			Metadata struct {
				Keywords []string `json:"keyword_filter"`
				Regex    []string `json:"regex_patterns"`
				Allowed  []string `json:"allow_list"`
			} `json:"trigger_metadata"`
			Roles    []string `json:"exempt_roles"`
			Channels []string `json:"exempt_channels"`
			Actions  []struct {
				Type     int
				Metadata struct {
					Channel string `json:"channel_id"`
					Custom  string `json:"custom_message"`
				}
			}
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return out, err
		}
		if !r.Enabled || r.Event != 1 || r.Trigger != 1 || len(r.Metadata.Regex)+len(r.Metadata.Allowed)+len(r.Roles)+len(r.Channels) > 0 {
			return out, fmt.Errorf("automod %q cannot be represented", r.Name)
		}
		a := AutoMod{Identity: Identity{ID: r.ID}, Name: r.Name, Keywords: r.Metadata.Keywords}
		for _, action := range r.Actions {
			switch action.Type {
			case 1:
				if action.Metadata.Custom != "" {
					return out, fmt.Errorf("custom automod message unsupported")
				}
				a.Block = true
			case 2:
				a.Alert = byID[action.Metadata.Channel]
				if a.Alert == "" {
					return out, fmt.Errorf("unknown alert channel")
				}
			default:
				return out, fmt.Errorf("automod action unsupported")
			}
		}
		out.Server.AutoMod = append(out.Server.AutoMod, a)
	}
	return out, nil
}

func permissions(raw string) ([]string, error) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	var out []string
	for name, bit := range PermissionBits {
		if n&bit != 0 {
			out = append(out, name)
			n &^= bit
		}
	}
	sort.Slice(out, func(i, j int) bool { return PermissionBits[out[i]] < PermissionBits[out[j]] })
	if n != 0 {
		return nil, fmt.Errorf("unsupported permission bits %x", n)
	}
	return out, nil
}
func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00\r\n") && s != "UNMANAGED.md"
}
