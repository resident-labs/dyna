package apply

import (
	"fmt"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/engine/discord/folder"
)

// addPrune deletes the objects apply owns that the folder no longer lists, and
// nothing else. It refuses before any write if a category it would delete holds a
// channel apply does not own, since Discord would leave that channel behind
// uncategorized. Standing messages are never deleted. Unmanaged entries,
// Discord-managed roles and @everyone are never candidates.
func (r *run) addPrune() error {
	listed := map[string]bool{}
	for _, id := range r.plan.Resolved {
		listed[id] = true
	}
	doomed := map[string]Owned{} // by Discord ID
	for _, owned := range r.owned {
		if owned.ID != "" && owned.Kind != string(folder.MessageKind) && !listed[owned.ID] && owned.ID != r.target.Guild {
			doomed[owned.ID] = owned
		}
	}
	var refusals []string
	var channels, categories, rules, roles []step
	forget := func(owned Owned) error { return r.target.Owned.Forget(r.ctx, owned.Kind, owned.Path) }
	remove := func(owned Owned, route string) step {
		return step{"delete " + owned.Kind + " " + owned.Path, func() error {
			if _, err := r.call("DELETE", route, nil); err != nil {
				return err
			}
			return forget(owned)
		}}
	}
	for _, id := range slices.Sorted(mapKeys(doomed)) {
		owned := doomed[id]
		switch folder.Kind(owned.Kind) {
		case folder.RoleKind:
			for _, role := range r.live.Server.Roles {
				if role.ID == id && !role.Managed {
					roles = append(roles, remove(owned, r.guildRoute("/roles/"+id)))
				}
			}
		case folder.AutoModKind:
			for _, rule := range r.live.Server.AutoMod {
				if rule.ID == id {
					rules = append(rules, remove(owned, r.guildRoute("/auto-moderation/rules/"+id)))
				}
			}
		case folder.ChannelKind, folder.CategoryKind:
			for _, entry := range r.live.Entries {
				if folder.EntryIdentity(entry).ID != id || entry.Unmanaged != "" {
					continue
				}
				if entry.Category == nil {
					channels = append(channels, remove(owned, "/channels/"+id))
					continue
				}
				for _, child := range r.live.Entries {
					if _, ours := doomed[folder.EntryIdentity(child).ID]; child.Parent == entry.Name && !ours {
						refusals = append(refusals, fmt.Sprintf("category %s holds %s, which apply does not own", entry.Path, child.Path))
					}
				}
				categories = append(categories, remove(owned, "/channels/"+id))
			}
		}
	}
	if len(refusals) > 0 {
		return fmt.Errorf("nothing was changed. Prune refused: %s", strings.Join(refusals, "; "))
	}
	r.phases[prunePhase] = slices.Concat(channels, categories, rules, roles)
	return nil
}
