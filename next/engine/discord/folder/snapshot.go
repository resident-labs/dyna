package folder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/engine/discord"
)

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
	return ReadManagedSnapshot(ctx, client, server, nil)
}

// ReadManagedSnapshot ignores unmanaged configuration while projecting the bulk
// guild/channel response; no per-channel reads are made for unmanaged entries.
func ReadManagedSnapshot(ctx context.Context, client *discord.Client, server string, entries []Entry) (Snapshot, error) {
	var out Snapshot
	if server == "" || strings.Trim(server, "0123456789") != "" {
		return out, fmt.Errorf("invalid guild ID")
	}
	get := func(suffix string, target any) error {
		result, err := client.ReadConfiguration(ctx, server, suffix)
		if err != nil {
			return err
		}
		if result.Status != 200 {
			return fmt.Errorf("snapshot %s: HTTP %d", suffix, result.Status)
		}
		if err := json.Unmarshal(result.Body, target); err != nil {
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

	var err error
	var roleNames map[string]string
	out, roleNames, err = snapshotRoles(out, roles, server)
	if err != nil {
		return out, err
	}
	byID := map[string]string{}
	for _, channel := range channels {
		byID[channel.ID] = channel.Name
	}
	out, err = snapshotChannels(out, channels, roleNames, byID, entries)
	if err != nil {
		return out, err
	}
	for _, feature := range guild.Features {
		if feature == "COMMUNITY" {
			if byID[guild.Rules] == "" || byID[guild.Updates] == "" {
				return out, fmt.Errorf("community channels missing")
			}
			out.Server.Settings.Community = &Community{Rules: byID[guild.Rules], Updates: byID[guild.Updates]}
		}
	}

	out, err = snapshotAutoMod(out, rules, byID)
	if err != nil {
		return out, err
	}
	if err := validate(Folder{Server: out.Server, Entries: out.Entries}); err != nil {
		return out, err
	}
	return out, nil
}
