package folder

import "fmt"

// unmanagedChannelReason identifies configuration the folder cannot faithfully
// own. The marker deliberately leaves that live object and its settings alone.
func unmanagedChannelReason(channel liveChannel) (string, error) {
	switch channel.Type {
	case 0, 2, 4, 5, 15:
	default:
		return fmt.Sprintf("Discord channel type %d is not managed by this folder.", channel.Type), nil
	}
	if channel.NSFW {
		return "NSFW settings are not managed by this folder.", nil
	}
	if channel.Flags != 0 {
		return "Discord channel flags are not managed by this folder.", nil
	}
	if channel.DefaultThreadSlowmode != 0 || channel.DefaultLayout != 0 || channel.DefaultSort != nil || (len(channel.DefaultReaction) > 0 && string(channel.DefaultReaction) != "null") || (channel.DefaultArchive != 0 && channel.DefaultArchive != 1440) {
		return "Discord forum or thread defaults are not managed by this folder.", nil
	}
	for _, tag := range channel.Tags {
		if tag.Moderated || tag.EmojiID != nil || tag.EmojiName != nil {
			return "Discord forum tag metadata is not managed by this folder.", nil
		}
	}
	for _, overwrite := range channel.Overwrites {
		for _, raw := range []string{overwrite.Allow, overwrite.Deny} {
			_, other, err := splitPermissions(raw)
			if err != nil {
				return "", fmt.Errorf("channel %s overwrite: %w", channel.ID, err)
			}
			if other != 0 {
				return "Unknown Discord overwrite permission bits are not managed by this folder.", nil
			}
		}
	}
	return "", nil
}
