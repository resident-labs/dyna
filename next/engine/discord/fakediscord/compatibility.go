package fakediscord

// CompatibilityConfiguration exercises settings deliberately left unmanaged,
// while keeping representable channels in the same guild exportable.
func CompatibilityConfiguration() Configuration {
	configuration := CommunityConfiguration()
	configuration.Roles[1].Permissions = "9223372036854784000" // manage_messages and an opaque bit.
	configuration.Roles[1].Icon = "role-icon"
	configuration.Roles[1].UnicodeEmoji = "star"
	configuration.Channels = append(configuration.Channels,
		Channel{ID: "236", Name: "stage", Type: 13, Parent: "200", Position: 9},
		Channel{ID: "230", Name: "unknown-kind", Type: 99, Parent: "200", Position: 3},
		Channel{ID: "231", Name: "nsfw", Type: 0, NSFW: true, Parent: "200", Position: 4},
		Channel{ID: "232", Name: "forum-defaults", Type: 15, DefaultLayout: 1, Parent: "200", Position: 5},
		Channel{ID: "233", Name: "tag-metadata", Type: 15, Tags: []Tag{{ID: "401", Name: "tag", EmojiName: "star"}}, Parent: "200", Position: 6},
		Channel{ID: "234", Name: "flags", Type: 0, Flags: 16, Parent: "200", Position: 7},
		Channel{ID: "235", Name: "opaque-overwrite", Type: 0, Parent: "200", Position: 8, Overwrites: []Overwrite{{ID: "10", Type: 0, Allow: "9223372036854775808", Deny: "0"}}},
	)
	configuration.AutoMod = append(configuration.AutoMod,
		AutoMod{ID: "410", Name: "disabled", Event: 1, Trigger: 1, Enabled: false},
		AutoMod{ID: "411", Name: "preset", Event: 1, Trigger: 4, Enabled: true},
		AutoMod{ID: "412", Name: "regex", Event: 1, Trigger: 1, Enabled: true, Metadata: TriggerMetadata{Regex: []string{"spam.*"}}},
		AutoMod{ID: "413", Name: "timeout", Event: 1, Trigger: 1, Enabled: true, Actions: []Action{{Type: 3}}},
	)
	return configuration
}
