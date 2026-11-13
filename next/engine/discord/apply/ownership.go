package apply

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/resident-labs/dyna/engine/discord/folder"
)

// identityOf finds a folder object's id/was. Unmanaged entries and Discord-managed
// roles are never ours, so they report as not found.
func (r *run) identityOf(kind folder.Kind, name string) (folder.Identity, bool) {
	switch kind {
	case folder.RoleKind:
		for _, role := range r.desired.Server.Roles {
			if role.Name == name && !role.Managed {
				return role.Identity, true
			}
		}
	case folder.AutoModKind:
		for _, rule := range r.desired.Server.AutoMod {
			if rule.Name == name {
				return rule.Identity, true
			}
		}
	case folder.CategoryKind, folder.ChannelKind:
		for _, entry := range r.desired.Entries {
			if entry.Path == name && entry.Unmanaged == "" && ((entry.Category != nil) == (kind == folder.CategoryKind)) {
				return folder.EntryIdentity(entry), true
			}
		}
	}
	return folder.Identity{}, false
}

// liveMatches lists the live objects of a kind with this name, for matching a create
// whose outcome was not recorded.
func (r *run) liveMatches(kind folder.Kind, name string) []string {
	var found []string
	switch kind {
	case folder.RoleKind:
		for _, role := range r.live.Server.Roles {
			if role.Name == name && !role.Managed {
				found = append(found, role.ID)
			}
		}
	case folder.AutoModKind:
		for _, rule := range r.live.Server.AutoMod {
			if rule.Name == name {
				found = append(found, rule.ID)
			}
		}
	default:
		for _, entry := range r.live.Entries {
			if entry.Path == name && entry.Unmanaged == "" && ((entry.Category != nil) == (kind == folder.CategoryKind)) {
				found = append(found, folder.EntryIdentity(entry).ID)
			}
		}
	}
	return found
}

// claimRecorded finishes what an earlier run began. A recorded object whose folder
// entry lost its "id:" gets it back. A create whose outcome was unknown is matched
// to the one live object of that name; if there is none, the create never happened
// and is forgotten. A message create with unknown outcome cannot be matched
// without reading history, so it refuses.
func (r *run) claimRecorded() (folder.Folder, error) {
	changed := false
	for _, owned := range r.owned {
		kind := folder.Kind(owned.Kind)
		if kind == folder.MessageKind {
			if owned.ID != "" || !r.messageDesired(owned.Path) {
				continue
			}
			return r.desired, fmt.Errorf("message %s: an earlier post may have reached Discord, and its outcome is unknown. Look in the channel and delete any copy, then rename the file (for example to a new name) to post it again", owned.Path)
		}
		identity, found := r.identityOf(kind, owned.Path)
		if !found || identity.ID != "" || identity.Was != "" {
			continue
		}
		id := owned.ID
		if id == "" {
			matches := r.liveMatches(kind, owned.Path)
			if len(matches) > 1 {
				return r.desired, fmt.Errorf("%s %s: several objects of that name exist and the earlier create is unmatched; give the right one an id: in the folder", kind, owned.Path)
			}
			if len(matches) == 0 {
				if err := r.target.Owned.Forget(r.ctx, owned.Kind, owned.Path); err != nil {
					return r.desired, err
				}
				continue
			}
			id = matches[0]
			owned.ID = id
			if err := r.target.Owned.Record(r.ctx, owned); err != nil {
				return r.desired, err
			}
		}
		if err := folder.RecordID(r.target.Folder, kind, owned.Path, id); err != nil {
			return r.desired, err
		}
		changed = true
	}
	if !changed {
		return r.desired, nil
	}
	return folder.Load(r.target.Folder)
}

func (r *run) messageDesired(messagePath string) bool {
	channel, file := path.Split(messagePath)
	_, found := r.entryByPath(strings.TrimSuffix(channel, "/")).Messages[file]
	return found
}

func (r *run) entryByPath(entryPath string) folder.Entry {
	for _, entry := range r.desired.Entries {
		if entry.Path == entryPath {
			return entry
		}
	}
	return folder.Entry{}
}

// hydrateMessages tells the plan which standing messages exist, from the record.
// Message history is never read.
func (r *run) hydrateMessages() {
	for _, owned := range r.owned {
		if owned.Kind != string(folder.MessageKind) || owned.ID == "" {
			continue
		}
		var message folder.Message
		if json.Unmarshal([]byte(owned.Content), &message) != nil {
			continue
		}
		message.ID = owned.ID
		for index, entry := range r.live.Entries {
			if folder.EntryIdentity(entry).ID != owned.Channel {
				continue
			}
			if r.live.Entries[index].Messages == nil {
				r.live.Entries[index].Messages = map[string]folder.Message{}
			}
			r.live.Entries[index].Messages[path.Base(owned.Path)] = message
		}
	}
}

// refuseUnadopted refuses to change a live object that the folder only matches by
// name. An existing object is adopted with "id:" or "was:", never by accident.
func (r *run) refuseUnadopted() error {
	var refusals []string
	for subject, id := range r.plan.Resolved {
		identity, ours := r.identityOf(subject.Kind, subject.Name)
		if ours && identity.ID == "" && identity.Was == "" {
			refusals = append(refusals, fmt.Sprintf("%s %s already exists on the server (ID %s) but the folder does not adopt it; add id: %q to adopt it", subject.Kind, subject.Name, id, id))
		}
	}
	if len(refusals) > 0 {
		return fmt.Errorf("nothing was changed. %s", strings.Join(refusals, "; "))
	}
	return nil
}

// recordAdopted records every object the folder adopts, so prune knows it owns them.
func (r *run) recordAdopted() error {
	for subject, id := range r.plan.Resolved {
		if _, ours := r.identityOf(subject.Kind, subject.Name); !ours || id == r.target.Guild {
			continue
		}
		if err := r.target.Owned.Record(r.ctx, Owned{Kind: string(subject.Kind), Path: subject.Name, ID: id}); err != nil {
			return err
		}
	}
	return nil
}
