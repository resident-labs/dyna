// Package reconcile applies a validated Discord folder with durable ownership.
package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/resident-labs/dyna/engine/discord/folder"
)

// Owned is a successful create/adoption receipt. Content is the last applied
// message, not message history; other objects are always compared with live state.
type Owned struct {
	Kind    folder.Kind
	Name    string
	ID      string
	Channel string
	Hash    string
	Content json.RawMessage
}

type Repository interface {
	Ownership(context.Context, string) ([]Owned, error)
	Record(context.Context, string, Owned) error
	Forget(context.Context, string, folder.Kind, string) error
}

func contentHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// Hydrate recovers identities previously created from this source and projects
// only recorded messages. It never reads Discord history or adopts by name alone.
func Hydrate(desired *folder.Folder, live *folder.Snapshot, owned []Owned) error {
	bySource := map[string]Owned{}
	for _, row := range owned {
		bySource[string(row.Kind)+":"+row.Name] = row
	}
	for index := range desired.Server.Roles {
		role := &desired.Server.Roles[index]
		if role.ID == "" && role.Was == "" {
			role.ID = bySource[string(folder.RoleKind)+":"+role.Name].ID
		}
	}
	for index := range desired.Server.AutoMod {
		rule := &desired.Server.AutoMod[index]
		if rule.ID == "" && rule.Was == "" {
			rule.ID = bySource[string(folder.AutoModKind)+":"+rule.Name].ID
		}
	}
	for index := range desired.Entries {
		entry := &desired.Entries[index]
		if entry.Unmanaged != "" {
			continue
		}
		var identity *folder.Identity
		kind := folder.ChannelKind
		if entry.Category != nil {
			identity = &entry.Category.Identity
			kind = folder.CategoryKind
		} else {
			identity = &entry.Channel.Identity
		}
		if identity.ID == "" && identity.Was == "" {
			identity.ID = bySource[string(kind)+":"+entry.Path].ID
		}
	}
	for index := range live.Entries {
		entry := &live.Entries[index]
		if entry.Channel == nil || entry.Unmanaged != "" {
			continue
		}
		for _, row := range owned {
			if row.Kind != folder.MessageKind || row.Channel != entry.Channel.ID {
				continue
			}
			var message folder.Message
			if err := json.Unmarshal(row.Content, &message); err != nil {
				return fmt.Errorf("owned message %s: %w", row.ID, err)
			}
			message.ID = row.ID
			if entry.Messages == nil {
				entry.Messages = map[string]folder.Message{}
			}
			entry.Messages[filepath.Base(row.Name)] = message
		}
	}
	return nil
}
