package folder

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

func snapshotChannels(out Snapshot, channels []liveChannel, roleNames, byID map[string]string, entries []Entry) (Snapshot, error) {
	sort.SliceStable(channels, func(i, j int) bool { return channels[i].Position < channels[j].Position })
	channelNames := map[string]string{}
	unmanaged := map[string]string{}
	for _, entry := range entries {
		if entry.Unmanaged != "" {
			unmanaged[entry.Path] = entry.Unmanaged
		}
	}
	for _, channel := range channels {
		parent := byID[channel.ParentID]
		path := channel.Name
		if parent != "" {
			path = parent + "/" + channel.Name
		}
		if unmanaged[path] != "" || unmanaged[parent] != "" {
			continue
		}
		reason, err := unmanagedChannelReason(channel)
		if err != nil {
			return out, err
		}
		if reason != "" {
			unmanaged[path] = reason
		}
	}
	for _, channel := range channels {
		parent := byID[channel.ParentID]
		path := channel.Name
		if parent != "" {
			path = parent + "/" + channel.Name
		}
		if unmanaged[parent] != "" {
			continue
		}
		key := channel.ParentID + "/" + channel.Name
		if !safeName(channel.Name) {
			return out, fmt.Errorf("unsafe channel/category name %q (ID %s)", channel.Name, channel.ID)
		}
		if previous, exists := channelNames[key]; exists {
			return out, fmt.Errorf("duplicate channel/category name %q under %s: IDs %s and %s", channel.Name, channel.ParentID, previous, channel.ID)
		}
		channelNames[key] = channel.ID
		if channel.Type == 4 && channel.ParentID != "" {
			return out, fmt.Errorf("nested category %s (ID %s)", channel.Name, channel.ID)
		}
		if note := unmanaged[path]; note != "" {
			entry := Entry{Name: channel.Name, Parent: parent, Path: path, Unmanaged: note}
			if channel.Type == 4 {
				entry.Category = &Category{Identity: Identity{ID: channel.ID}}
				out.Server.Categories = append(out.Server.Categories, channel.Name)
			} else {
				entry.Channel = &Channel{Identity: Identity{ID: channel.ID}, Type: "text"}
			}
			out.Entries = append(out.Entries, entry)
			continue
		}
		overwrites := Overwrites{}
		for _, overwrite := range channel.Overwrites {
			name := roleNames[overwrite.ID]
			if overwrite.Type == 1 {
				name = "member:" + overwrite.ID
			} else if overwrite.Type != 0 || name == "" {
				return out, fmt.Errorf("unknown overwrite target")
			}
			allow, err := permissions(overwrite.Allow)
			if err != nil {
				return out, err
			}
			deny, err := permissions(overwrite.Deny)
			if err != nil {
				return out, err
			}
			if len(allow) > 0 {
				if overwrites.Allow == nil {
					overwrites.Allow = map[string][]string{}
				}
				overwrites.Allow[name] = allow
			}
			if len(deny) > 0 {
				if overwrites.Deny == nil {
					overwrites.Deny = map[string][]string{}
				}
				overwrites.Deny[name] = deny
			}
		}
		entry := Entry{Name: channel.Name, Path: channel.Name}
		if channel.Type == 4 {
			entry.Category = &Category{Identity: Identity{ID: channel.ID}, Overwrites: overwrites}
			out.Server.Categories = append(out.Server.Categories, channel.Name)
		} else {
			kind := map[int]string{0: "text", 2: "voice", 5: "announcement", 15: "forum"}[channel.Type]
			config := &Channel{Identity: Identity{ID: channel.ID}, Overwrites: overwrites, Type: kind, Topic: channel.Topic}
			if channel.Slowmode != 0 {
				config.Slowmode = strconv.Itoa(channel.Slowmode) + "s"
			}
			tagNames := map[string]bool{}
			for _, tag := range channel.Tags {
				if tag.Name == "" || tagNames[tag.Name] {
					return out, fmt.Errorf("ambiguous forum tag %q", tag.Name)
				}
				tagNames[tag.Name] = true
				config.Tags = append(config.Tags, tag.Name)
			}
			entry.Channel = config
			if channel.ParentID != "" {
				parent := byID[channel.ParentID]
				isCategory := false
				for _, candidate := range channels {
					if candidate.ID == channel.ParentID && candidate.Type == 4 {
						isCategory = true
					}
				}
				if parent == "" || !isCategory {
					return out, fmt.Errorf("missing parent")
				}
				entry.Parent = parent
				entry.Path = parent + "/" + channel.Name
			}
		}
		out.Entries = append(out.Entries, entry)
	}
	for i := range out.Entries {
		entry := &out.Entries[i]
		if entry.Category != nil {
			entry.Category.Sync = true
			for _, child := range out.Entries {
				if child.Parent == entry.Name {
					entry.Category.Channels = append(entry.Category.Channels, child.Name)
					if child.Channel != nil && !reflect.DeepEqual(child.Channel.Overwrites, entry.Category.Overwrites) {
						entry.Category.Sync = false
					}
				}
			}
		}
	}

	return out, nil
}
