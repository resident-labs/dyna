package reconcile

import (
	"fmt"

	"github.com/resident-labs/dyna/engine/discord/folder"
)

type bindings struct {
	roles        map[string]string
	channels     map[string]string
	channelNames map[string][]string
}

func bind(desired folder.Folder, live folder.Snapshot, server string) (bindings, error) {
	references := bindings{roles: map[string]string{"everyone": server}, channels: map[string]string{}, channelNames: map[string][]string{}}
	for _, role := range desired.Server.Roles {
		for _, current := range live.Server.Roles {
			if !identityMatches(role.Identity, role.Name, current.ID, current.Name) {
				continue
			}
			if role.ID == "" && role.Was == "" {
				return references, fmt.Errorf("role %s already exists; adopt it explicitly with id or was", role.Name)
			}
			references.roles[role.Name] = current.ID
		}
	}
	parents := map[string]string{"": ""}
	for _, entry := range desired.Entries {
		if entry.Category == nil {
			continue
		}
		parents[entry.Name] = entry.Name
		for _, current := range live.Entries {
			if current.Category != nil && identityMatches(entry.Category.Identity, entry.Name, current.Category.ID, current.Name) {
				parents[entry.Name] = current.Name
			}
		}
	}
	for _, entry := range desired.Entries {
		if entry.Unmanaged != "" {
			continue
		}
		identity := folder.Identity{}
		if entry.Category != nil {
			identity = entry.Category.Identity
		} else {
			identity = entry.Channel.Identity
		}
		for _, current := range live.Entries {
			if current.Unmanaged != "" || (entry.Category == nil) != (current.Category == nil) {
				continue
			}
			currentID := ""
			if current.Category != nil {
				currentID = current.Category.ID
			} else {
				currentID = current.Channel.ID
			}
			if !identityMatches(identity, entry.Name, currentID, current.Name) || identity.ID == "" && current.Parent != parents[entry.Parent] {
				continue
			}
			if identity.ID == "" && identity.Was == "" {
				return references, fmt.Errorf("%s already exists; adopt it explicitly with id or was", entry.Path)
			}
			references.channels[entry.Path] = currentID
		}
	}
	for _, entry := range desired.Entries {
		if entry.Channel != nil && entry.Unmanaged == "" {
			references.channelNames[entry.Name] = append(references.channelNames[entry.Name], entry.Path)
		}
	}
	return references, nil
}

func identityMatches(identity folder.Identity, name, id, currentName string) bool {
	if identity.ID != "" {
		return identity.ID == id
	}
	return name == currentName || identity.Was != "" && identity.Was == currentName
}

func (references bindings) channel(name string) (string, error) {
	paths := references.channelNames[name]
	if len(paths) != 1 {
		return "", fmt.Errorf("channel %q is missing or not unique", name)
	}
	id := references.channels[paths[0]]
	if id == "" {
		return "", fmt.Errorf("channel %q has not been created", name)
	}
	return id, nil
}
